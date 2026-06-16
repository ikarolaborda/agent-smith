/*
cluster.Provider is the single seam that joins this whole package to the rest of
agent-smith. It implements llm.Provider, so the existing agent loop, HTTP server,
and CLI treat the cluster as just another provider named "cluster" with no other
changes. Chat/ChatStream resolve a backend via the scheduler and stream tokens
through it; the wrapped local provider is the guaranteed fallback.
*/
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* providerName is the registry key for the cluster provider. */
const providerName = "cluster"

/* Provider is the llm.Provider façade over the cluster control plane. */
type Provider struct {
	cfg     *ClusterConfig
	mgr     *Manager
	sched   Scheduler
	logger  *slog.Logger
	metrics *Collector
	/* defaultModel is the model id used when a request carries no model. */
	defaultModel string
	/* stopRefresh cancels the background node-health refresh started in New. */
	stopRefresh context.CancelFunc
}

/*
New builds a cluster Provider from a loaded ClusterConfig. localProvider is the
existing single-node provider that backs the local fallback (may be nil only if
strict_cluster is set). On construction it discovers nodes so the scheduler's
memory math reflects which workers are actually reachable.
*/
func New(ctx context.Context, cfg *ClusterConfig, localProvider llm.Provider, logger *slog.Logger) (*Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cluster: nil config")
	}
	if localProvider == nil && !cfg.Runtime.StrictCluster {
		logger.Warn("cluster: no local provider wired; local fallback will be unavailable")
	}
	metrics := NewCollector()
	mgr := NewManager(cfg, localProvider, logger, metrics)
	sched := newScheduler(cfg, mgr, logger)

	if _, err := mgr.Discover(ctx); err != nil {
		logger.Warn("cluster: discovery failed (continuing)", "err", err)
	}

	defaultModel := ""
	if len(cfg.Models) > 0 {
		defaultModel = cfg.Models[0].ID
	}
	/*
		Refresh node reachability in the background so the UI status badge reflects
		a worker that dropped/rebooted, not just the startup snapshot. Detached from
		the request ctx (which may be short-lived) and stopped in Close.
	*/
	refreshCtx, cancel := context.WithCancel(context.Background())
	go mgr.healthRefreshLoop(refreshCtx, nodeHealthRefreshInterval)
	return &Provider{
		cfg:          cfg,
		mgr:          mgr,
		sched:        sched,
		logger:       logger,
		metrics:      metrics,
		defaultModel: defaultModel,
		stopRefresh:  cancel,
	}, nil
}

/* Name implements llm.Provider. */
func (p *Provider) Name() string { return providerName }

/*
ModelInfo is the picker-facing view of one configured cluster model. ID is the
config id the chat route sends back as the model (resolveModel maps it to the
backend's served_name); ContextTokens is the effective window the cluster
applies for that model. IsDefault marks the model served when a request carries
no model id.
*/
type ModelInfo struct {
	ID            string
	ContextTokens int
	IsDefault     bool
}

/*
ListModels enumerates the models declared in the cluster config so the HTTP
server can surface them in the model picker as "cluster/<id>" entries. Without
this the server can only synthesize a single empty "cluster/" entry from the app
config (which has no cluster provider block), leaving no way to select the
clustered model from the web UI.
*/
func (p *Provider) ListModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(p.cfg.Models))
	for _, m := range p.cfg.Models {
		out = append(out, ModelInfo{
			ID:            m.ID,
			ContextTokens: m.ContextTokens,
			IsDefault:     m.ID == p.defaultModel,
		})
	}
	return out
}

/* Manager exposes the underlying manager for CLI/diagnostic use. */
func (p *Provider) Manager() *Manager { return p.mgr }

/* Metrics exposes the shared metrics collector. */
func (p *Provider) Metrics() *Collector { return p.metrics }

/* Close stops the background refresh and every started backend. */
func (p *Provider) Close(ctx context.Context) error {
	if p.stopRefresh != nil {
		p.stopRefresh()
	}
	return p.mgr.StopAll(ctx)
}

/*
resolveModel maps the request's model field onto a configured ModelConfig. The
request may name a config model id directly, or be empty (use the default).
Unknown ids fall back to the default model rather than failing, so a UI that
sends raw model names still works.
*/
func (p *Provider) resolveModel(req llm.ChatRequest) (ModelConfig, error) {
	id := req.Model
	if id == "" {
		id = p.defaultModel
	}
	if mc, ok := p.cfg.ModelByID(id); ok {
		return mc, nil
	}
	if mc, ok := p.cfg.ModelByID(p.defaultModel); ok {
		return mc, nil
	}
	return ModelConfig{}, fmt.Errorf("cluster: no configured model for %q and no default", req.Model)
}

/*
Chat is the non-streaming entrypoint. It runs ChatStream internally and
accumulates the deltas, so non-streaming callers get the same routing, fallback,
and metrics behavior as streaming ones.
*/
/*
applyContextWindow propagates a model's configured context_tokens to the request
as NumCtx so a single-node backend (Ollama) serves the large window the model was
configured for, rather than silently using the runtime's small default — the
window matters for long-context work like vulnerability research. It logs the
effective context so a silent downgrade is visible. Per-request NumCtx wins.
*/
func (p *Provider) applyContextWindow(req *llm.ChatRequest, model ModelConfig, backendName string) {
	if model.ContextTokens <= 0 {
		return
	}
	if req.NumCtx == nil {
		ctx := model.ContextTokens
		req.NumCtx = &ctx
	}
	p.logger.Info("cluster: effective context window",
		"model", model.ID, "backend", backendName,
		"context_tokens", *req.NumCtx, "single_node", backendName == BackendLocal)
}

func (p *Provider) Chat(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	model, err := p.resolveModel(req)
	if err != nil {
		return nil, err
	}
	backend, err := p.sched.SelectBackend(ctx, req, model)
	if err != nil {
		return nil, err
	}
	req.Model = model.ServedName
	p.applyContextWindow(&req, model, backend.Name())

	/*
		The streaming transport emits a cumulative snapshot of each in-progress
		tool call (keyed by the provider's tool-call index) on every fragment,
		so the latest snapshot for a given call id is the complete call. We keep
		the newest snapshot per id (preserving first-seen order) rather than
		appending every fragment, which would otherwise yield duplicate partial
		tool calls in the aggregated non-streaming response.
	*/
	var content strings.Builder
	byID := map[string]*llm.ToolCall{}
	var order []string
	sink := TokenStreamFunc(func(c llm.StreamChunk) {
		content.WriteString(c.Delta)
		if c.ToolCallDelta == nil {
			return
		}
		snap := *c.ToolCallDelta
		if existing, ok := byID[snap.ID]; ok {
			*existing = snap
			return
		}
		cp := snap
		byID[snap.ID] = &cp
		order = append(order, snap.ID)
	})
	if err := backend.Chat(ctx, req, sink); err != nil {
		return nil, err
	}

	var toolCalls []llm.ToolCall
	for _, id := range order {
		toolCalls = append(toolCalls, *byID[id])
	}
	finish := "stop"
	if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	return &llm.ChatResponse{
		Message:      llm.Message{Role: llm.RoleAssistant, Content: content.String(), ToolCalls: toolCalls},
		FinishReason: finish,
	}, nil
}

/*
ChatStream selects a backend and streams its output onto the returned channel,
exactly matching the contract of llm.Provider.ChatStream (channel closed by the
producer; a final chunk carries Err on failure).
*/
func (p *Provider) ChatStream(ctx context.Context, req llm.ChatRequest) (<-chan llm.StreamChunk, error) {
	model, err := p.resolveModel(req)
	if err != nil {
		return nil, err
	}
	backend, err := p.sched.SelectBackend(ctx, req, model)
	if err != nil {
		return nil, err
	}
	req.Model = model.ServedName
	p.applyContextWindow(&req, model, backend.Name())

	out := make(chan llm.StreamChunk, 16)
	go func() {
		defer close(out)
		sink := TokenStreamFunc(func(c llm.StreamChunk) { out <- c })
		if err := backend.Chat(ctx, req, sink); err != nil {
			out <- llm.StreamChunk{Err: err}
			return
		}
		out <- llm.StreamChunk{Done: true}
	}()
	return out, nil
}

/*
Register wires the cluster provider into the llm registry. The factory expects a
*Provider already constructed by New (the cluster needs a context + local
fallback that the generic registry cannot supply), so callers normally use New
directly; Register exists for symmetry and for code paths that only have the
registry.
*/
func Register(p *Provider) {
	llm.Register(providerName, func(cfg any) (llm.Provider, error) {
		if p == nil {
			return nil, fmt.Errorf("cluster: provider not constructed")
		}
		return p, nil
	})
}
