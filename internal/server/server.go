/*
Package server exposes a single-binary HTTP frontend for agent-smith. It hosts
an OpenAI-compatible /v1/chat/completions endpoint that streams responses via
Server-Sent Events, lightweight /v1/models and /v1/providers discovery routes,
and an embedded React SPA built from /web.
*/
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/abliteration"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/refine"
	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
	"github.com/ikarolaborda/agent-smith/internal/validate"
	"github.com/ikarolaborda/agent-smith/internal/verify"
)

/* Options carries the configuration needed to build a Server. */
type Options struct {
	Addr   string
	Config *config.Config
	/*
		Workspace is the initial folder the agentic file_write/file_edit tools
		may mutate (empty = read-only). It can be changed at runtime via
		POST /v1/workspace; the agent's tools are rebuilt per request from the
		current value.
	*/
	Workspace string
	/*
		AllowExec enables the opt-in container-contained execution tool (ADR
		0003) on each per-request agent. Off by default; requires a workspace to
		mount and Docker on the host.
	*/
	AllowExec bool
	/*
		ExecImageDigest, when set, pins the contained-exec apparatus image to an
		exact local image ID (sha256:<hex>) so resolution fails closed on a local
		re-tag. Empty = resolve by tag. Mirrors the CLI --exec-image-digest flag.
	*/
	ExecImageDigest string
	Logger          *slog.Logger
	/*
		Providers, when non-nil, replaces the providers New would build from
		Config.Providers. Used by tests to inject fake provider clients;
		production callers should leave it nil.
	*/
	Providers map[string]llm.Provider
	/*
		ExtraProviders are additive: unlike Providers (which replaces the
		config-built set), these are merged on TOP of the providers built from
		Config.Providers, so a self-managed provider like llamacpp — whose
		subprocess lifecycle is owned by the caller, not the server — can coexist
		with the configured cloud/ollama providers. An extra provider overrides a
		config-built one of the same name.
	*/
	ExtraProviders map[string]llm.Provider
	AllowedOrigins []string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	/* RAG, when non-nil, is the retrieval service consulted by chat handlers. */
	RAG *rag.Service
	/* DisableRAG, when true, suppresses augmentation regardless of RAG presence. */
	DisableRAG bool
	/*
		Agentic enables agentic-RAG by default: the reasoning model plans and runs
		its own retrieval via the rag_search tool and self-evaluates, instead of the
		one-shot Augment. Per-request `agentic` in the chat body overrides it. Only
		takes effect when RAG is live and the provider supports tools.
	*/
	Agentic bool
	/*
		WebSearchEnabled is the operator-level kill switch for the web
		grounding gate. When false, the chat handler never injects web
		results regardless of per-request or provider-default flags. When
		true (default), Ollama requests get web grounding ON by default
		and cloud providers OFF; the request body's web_search field can
		flip either way.
	*/
	WebSearchEnabled bool
	/*
		VerifyCVE enables the NVD primary-source verification gate: the agent
		appends a non-destructive advisory note when an answer cites a CVE. The
		NVD apiKey is read from NVD_API_KEY in the environment when present
		(optional; it raises NVD's rate limit). Default false preserves the
		offline-first posture.
	*/
	VerifyCVE bool
	/*
		ValidateVuln enables the cross-provider validation layer: vulnerability-
		research answers are critiqued by independent models (OpenAI via its API,
		Anthropic via the Claude Code CLI / Max subscription) and a non-authoritative
		advisory is appended. Default false; opt-in because it incurs egress, cost,
		and drives the Max subscription programmatically.
	*/
	ValidateVuln bool
}

/*
Server bundles the HTTP listener, route mux, configured providers, and tool
registry. Each chat request gets its own agent.Agent instance built from an
already-constructed provider, so concurrent streams do not share mutable
state.
*/
type Server struct {
	addr      string
	cfg       *config.Config
	logger    *slog.Logger
	providers map[string]llm.Provider
	models    []modelEntry
	mux       *http.ServeMux
	/*
		workspace is the folder the agentic file tools are scoped to; guarded by
		workspaceMu because it is read on every chat request and written by the
		POST /v1/workspace handler. Empty = read-only (no file_write/file_edit).
	*/
	workspace   string
	workspaceMu sync.RWMutex
	/* allowExec enables the opt-in contained exec tool on per-request agents (ADR 0003). */
	allowExec bool
	/* execImageDigest optionally pins the apparatus image by local image ID (ADR 0003). */
	execImageDigest  string
	allowedOrigins   map[string]struct{}
	readTimeout      time.Duration
	writeTimeout     time.Duration
	rag              *rag.Service
	disableRAG       bool
	agentic          bool
	webSearchEnabled bool
	/*
		answerVerifier is the shared post-answer advisory layer (NVD CVE check
		and/or cross-provider validation) attached to each per-request agent when
		configured (nil = off). Shared so the NVD cache and reviewer clients
		persist across requests.
	*/
	answerVerifier agent.Verifier

	/*
		refineJudge is the strict judge for opt-in refine mode (judge-in-the-loop).
		Built once from the openai provider config; nil when unavailable, in which
		case a refine request hard-fails rather than returning an unevaluated answer.
	*/
	refineJudge refine.Judge

	/*
		modelsMu guards the dynamic Ollama model list. We preload it at
		boot and refresh on /v1/models GET when the TTL elapses; if the
		refresh call fails, we keep serving the last known list.
	*/
	modelsMu       sync.Mutex
	dynamicModels  []modelEntry
	dynamicExpires time.Time

	/* visionCache memoizes Ollama model name -> vision support so /api/show is queried at most once per model. */
	visionCache sync.Map
}

/* modelsCacheTTL controls how often /v1/models re-queries Ollama for installed models. */
const modelsCacheTTL = 60 * time.Second

/* modelEntry is the public shape of a /v1/models row. */
type modelEntry struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Created  int64  `json:"created"`
	OwnedBy  string `json:"owned_by"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	/*
		Kind is "chat" or "embedding" for Ollama models discovered via
		/api/tags; empty for cloud providers and the configured default
		row. The UI uses it to filter the chat-model dropdown.
	*/
	Kind string `json:"kind,omitempty"`
	/*
		SupportsVision is true when the model accepts image input. For Ollama
		it comes from /api/show capabilities; for cloud providers it is a
		conservative per-model heuristic. The UI uses it to gate image paste.
	*/
	SupportsVision bool `json:"supports_vision,omitempty"`
}

/*
New builds a Server from Options, constructing one provider client per entry
in cfg.Providers. Missing or invalid provider blocks are logged and skipped
rather than aborting startup, so the UI still loads when one credential is
absent.
*/
func New(opts Options) (*Server, error) {
	if opts.Config == nil {
		return nil, errors.New("server: config is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Addr == "" {
		/*
			The API has profile-scoped memory and mutating agent tools but no
			authentication boundary. Keep a default launch local to the host;
			operators who deliberately put it behind an authenticated reverse proxy
			can still opt into an all-interface address explicitly.
		*/
		opts.Addr = "127.0.0.1:9090"
	}

	s := &Server{
		addr:             opts.Addr,
		cfg:              opts.Config,
		workspace:        opts.Workspace,
		allowExec:        opts.AllowExec,
		execImageDigest:  opts.ExecImageDigest,
		logger:           logger,
		providers:        map[string]llm.Provider{},
		mux:              http.NewServeMux(),
		allowedOrigins:   map[string]struct{}{},
		readTimeout:      opts.ReadTimeout,
		writeTimeout:     opts.WriteTimeout,
		rag:              opts.RAG,
		disableRAG:       opts.DisableRAG,
		agentic:          opts.Agentic,
		webSearchEnabled: opts.WebSearchEnabled,
	}
	s.answerVerifier = BuildAnswerVerifier(opts.Config, opts.VerifyCVE, opts.ValidateVuln, logger)
	s.refineJudge = buildRefineJudge(opts.Config)
	for _, o := range opts.AllowedOrigins {
		s.allowedOrigins[o] = struct{}{}
	}

	created := time.Now().Unix()
	if opts.Providers != nil {
		for name, prov := range opts.Providers {
			s.providers[name] = prov
			/*
				A provider that declares its own models (the cluster provider, whose
				models live in the cluster YAML, not the app config) gets one picker
				entry per declared model. Otherwise the app config supplies the single
				model name. Without this the cluster provider yields a lone "cluster/"
				entry with an empty model — there is no way to pick the clustered model
				from the web UI, and chat silently falls through to the local backend.
			*/
			if lister, ok := prov.(clusterModelLister); ok {
				for _, m := range lister.ListModels() {
					s.models = append(s.models, modelEntry{
						ID:       name + "/" + m.ID,
						Object:   "model",
						Created:  created,
						OwnedBy:  name,
						Provider: name,
						Model:    m.ID,
						Kind:     "chat",
					})
				}
				continue
			}
			model := ""
			if cfg, ok := opts.Config.Providers[name]; ok {
				model = cfg.Model
			}
			s.models = append(s.models, modelEntry{
				ID:             name + "/" + model,
				Object:         "model",
				Created:        created,
				OwnedBy:        name,
				Provider:       name,
				Model:          model,
				SupportsVision: providerSupportsVision(prov, name, model),
			})
		}
	} else {
		for name, p := range opts.Config.Providers {
			/*
				llamacpp is self-managed: it needs a downloaded model and a
				supervised llama-server subprocess, which is built and owned by
				the caller and injected via ExtraProviders. Skip it here so the
				config-path builder does not report it as unavailable.
			*/
			if name == "llamacpp" {
				continue
			}
			prov, err := buildProvider(name, p)
			if err != nil {
				logger.Warn("provider unavailable", "provider", name, "err", err)
				continue
			}
			s.providers[name] = prov
			s.models = append(s.models, modelEntry{
				ID:             name + "/" + p.Model,
				Object:         "model",
				Created:        created,
				OwnedBy:        name,
				Provider:       name,
				Model:          p.Model,
				SupportsVision: cloudModelSupportsVision(name, p.Model),
			})
			logger.Info("provider ready", "provider", name, "model", p.Model)
		}
	}
	for name, prov := range opts.ExtraProviders {
		s.providers[name] = prov
		model := ""
		if cfg, ok := opts.Config.Providers[name]; ok {
			model = cfg.Model
		}
		s.models = append(s.models, modelEntry{
			ID:             name + "/" + model,
			Object:         "model",
			Created:        created,
			OwnedBy:        name,
			Provider:       name,
			Model:          model,
			SupportsVision: providerSupportsVision(prov, name, model),
		})
		logger.Info("provider ready", "provider", name, "model", model)
	}

	if len(s.providers) == 0 {
		logger.Warn("no providers ready — chat completions will return 503")
	}

	s.registerRoutes()
	s.refreshOllamaModels(context.Background())
	return s, nil
}

/*
refreshOllamaModels queries /api/tags via the registered ollama Client (if any)
and rebuilds the dynamic model list. Failures are logged and leave the
previous list intact, so /v1/models stays useful when Ollama is briefly
unavailable.
*/
func (s *Server) refreshOllamaModels(ctx context.Context) {
	prov, ok := s.providers["ollama"]
	if !ok {
		return
	}
	lister, ok := prov.(interface {
		ListModels(ctx context.Context) ([]ollama.ModelInfo, error)
	})
	if !ok {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	models, err := lister.ListModels(listCtx)
	if err != nil {
		s.logger.Warn("ollama: ListModels failed; keeping previous cache", "err", err)
		return
	}
	capLister, _ := prov.(interface {
		Capabilities(ctx context.Context, model string) ([]string, error)
	})

	created := time.Now().Unix()
	var rows []modelEntry
	for _, m := range models {
		kind := "chat"
		if m.Embedding {
			kind = "embedding"
		}
		vision := false
		if kind == "chat" && capLister != nil {
			vision = s.ollamaModelVision(ctx, capLister, m.Name)
		}
		rows = append(rows, modelEntry{
			ID:             "ollama/" + m.Name,
			Object:         "model",
			Created:        created,
			OwnedBy:        "ollama",
			Provider:       "ollama",
			Model:          m.Name,
			Kind:           kind,
			SupportsVision: vision,
		})
	}
	s.modelsMu.Lock()
	s.dynamicModels = rows
	s.dynamicExpires = time.Now().Add(modelsCacheTTL)
	s.modelsMu.Unlock()
	s.logger.Info("ollama: dynamic models refreshed", "count", len(rows))
}

/*
ollamaModelVision reports whether an Ollama model advertises the "vision"
capability, memoizing the result so /api/show is queried at most once per
model. A failed lookup returns false and is not cached, so a transiently
unavailable daemon does not pin a model to "no vision" permanently.
*/
func (s *Server) ollamaModelVision(ctx context.Context, capLister interface {
	Capabilities(ctx context.Context, model string) ([]string, error)
}, model string) bool {
	if v, ok := s.visionCache.Load(model); ok {
		return v.(bool)
	}
	showCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	caps, err := capLister.Capabilities(showCtx, model)
	if err != nil {
		s.logger.Debug("ollama: capability probe failed", "model", model, "err", err)
		return false
	}
	vision := false
	for _, c := range caps {
		if strings.EqualFold(c, "vision") {
			vision = true
			break
		}
	}
	s.visionCache.Store(model, vision)
	return vision
}

/*
cloudModelSupportsVision is a conservative per-model heuristic for the cloud
providers, which expose no capability endpoint. All current Anthropic Claude
chat models accept images; for OpenAI, the multimodal families are matched by
substring.
*/
func cloudModelSupportsVision(provider, model string) bool {
	m := strings.ToLower(model)
	switch provider {
	case "anthropic":
		return strings.Contains(m, "claude")
	case "openai":
		for _, marker := range []string{"gpt-4o", "gpt-4.1", "gpt-4-turbo", "gpt-5", "o1", "o3", "o4", "chatgpt-4o"} {
			if strings.Contains(m, marker) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

/*
providerSupportsVision lets a self-managed runtime report a capability learned
from its resolved artifacts (for example, a llama.cpp model with a validated
mmproj) instead of guessing from the model name. Cloud clients do not expose
this method, so they retain the conservative name-based fallback.
*/
/*
clusterModelLister is the consumer-side capability the server needs from a
provider that fronts several named models (the cluster provider): the ability to
enumerate them so each can be surfaced individually in the model list. It is
defined here, at the point of use, rather than in internal/llm, so the provider
contract does not take a dependency on cluster.ModelInfo.
*/
type clusterModelLister interface {
	ListModels() []cluster.ModelInfo
}

func providerSupportsVision(prov llm.Provider, provider, model string) bool {
	if capable, ok := prov.(llm.VisionReporter); ok {
		return capable.SupportsVision()
	}
	return cloudModelSupportsVision(provider, model)
}

/*
listModels returns the merged static + dynamic model list, refreshing the
Ollama cache when the TTL has elapsed. Non-Ollama static rows are always
included; Ollama static rows are replaced by dynamic rows when discovery has
succeeded at least once.
*/
func (s *Server) listModels(ctx context.Context) []modelEntry {
	s.modelsMu.Lock()
	expired := time.Now().After(s.dynamicExpires)
	s.modelsMu.Unlock()
	if expired {
		s.refreshOllamaModels(ctx)
	}
	s.modelsMu.Lock()
	dyn := append([]modelEntry(nil), s.dynamicModels...)
	s.modelsMu.Unlock()

	out := make([]modelEntry, 0, len(s.models)+len(dyn))
	for _, m := range s.models {
		if m.Provider == "ollama" && len(dyn) > 0 {
			continue
		}
		out = append(out, m)
	}
	out = append(out, dyn...)
	return out
}

/* ListenAndServe binds the server to its address and blocks until ctx is cancelled. */
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:         s.addr,
		Handler:      s.Handler(),
		ReadTimeout:  s.readTimeout,
		WriteTimeout: s.writeTimeout,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	s.logger.Info("agent-smith http listening", "addr", s.addr, "providers", providerNames(s.providers))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

/* Handler exposes the configured mux. Useful for httptest. */
func (s *Server) Handler() http.Handler { return s.withCORS(s.mux) }

/* registerRoutes wires the supported paths onto the internal mux. */
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/healthz", s.handleHealth)
	s.mux.HandleFunc("/v1/cluster", s.handleClusterStatus)
	s.mux.HandleFunc("/v1/workspace", s.handleWorkspace)
	s.mux.HandleFunc("/v1/workspace/tree", s.handleWorkspaceTree)
	s.mux.HandleFunc("/v1/models", s.handleModels)
	s.mux.HandleFunc("/v1/models/search", s.handleModelSearch)
	s.mux.HandleFunc("/v1/system", s.handleSystem)
	s.mux.HandleFunc("/v1/providers", s.handleProviders)
	s.mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	s.mux.HandleFunc("/v1/title", s.handleTitle)
	s.mux.HandleFunc("/v1/rag/collections", s.handleRAGCollections)
	s.mux.HandleFunc("/v1/rag/search", s.handleRAGSearch)
	s.mux.HandleFunc("/v1/rag/remember", s.handleRAGRemember)
	s.mux.HandleFunc("/v1/rag/forget", s.handleRAGForget)
	s.mux.HandleFunc("/v1/rag/memory", s.handleRAGMemory)
	s.mux.HandleFunc("/v1/rag/correction", s.handleRAGCorrection)
	s.mux.Handle("/", staticHandler(s.logger))
}

/*
withCORS adds permissive CORS headers for browser-driven dev where the React
app is served by Vite on a different port. Origins outside the configured
allowlist receive no Access-Control-Allow-Origin header; same-origin requests
are unaffected.
*/
func (s *Server) withCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := s.allowedOrigins[origin]; ok || s.allowOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

/* allowOrigin checks an origin against allowlist defaults (localhost during dev). */
func (s *Server) allowOrigin(origin string) bool {
	if len(s.allowedOrigins) > 0 {
		_, ok := s.allowedOrigins[origin]
		return ok
	}
	/* default-deny in production-style boots; permissive for localhost only */
	return strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:")
}

/* providerNames returns the registered provider names, for log lines. */
func providerNames(m map[string]llm.Provider) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

/*
buildProvider constructs a single llm.Provider for one config entry. Missing
API keys for cloud providers return an explicit error so New can skip them
with a logged warning rather than failing startup.
*/
func buildProvider(name string, p config.ProviderConfig) (llm.Provider, error) {
	switch name {
	case "openai":
		if p.APIKey == "" {
			return nil, fmt.Errorf("missing api_key for %s", name)
		}
		return llm.New(name, openai.Config{APIKey: p.APIKey, BaseURL: p.BaseURL, Model: p.Model})
	case "abliteration":
		if p.APIKey == "" {
			return nil, fmt.Errorf("missing api_key for %s", name)
		}
		return llm.New(name, abliteration.Config{APIKey: p.APIKey, BaseURL: p.BaseURL, Model: p.Model})
	case "anthropic":
		if p.APIKey == "" {
			return nil, fmt.Errorf("missing api_key for %s", name)
		}
		return llm.New(name, anthropic.Config{APIKey: p.APIKey, BaseURL: p.BaseURL, Model: p.Model})
	case "ollama":
		return llm.New(name, ollama.Config{BaseURL: p.BaseURL, Model: p.Model})
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
}

/*
shouldAgentic resolves agentic-RAG for one request: never on when RAG is
unavailable (there would be no rag_search tool to drive), otherwise the
per-request override wins over the operator default.
*/
func (s *Server) shouldAgentic(requested *bool) bool {
	if s.rag == nil || s.disableRAG {
		return false
	}
	if requested != nil {
		return *requested
	}
	return s.agentic
}

/*
shouldWebSearch applies the precedence rules: operator kill switch first,
then per-request override, then the local/refusal-removed provider default.
*/
func (s *Server) shouldWebSearch(provName string, requested *bool) bool {
	if !s.webSearchEnabled {
		return false
	}
	if s.rag == nil || s.rag.WebSearch == nil {
		return false
	}
	if requested != nil {
		return *requested
	}
	/*
		Default web grounding ON for the providers most prone to fabrication when
		left ungrounded. The cluster and self-managed llamacpp providers run local
		models exactly like Ollama, so they need the same hallucination-
		suppressing grounding by default. The remote abliteration model is a
		refusal-removed model that — as observed in this lab — confidently
		fabricates CVE identifiers, CVSS scores, and version ranges when answered
		from weights alone; defaulting grounding ON is the proven suppressor for
		exactly that failure. The operator kill switch (--no-web-search) and the
		per-request override both still take precedence over this default.
	*/
	return provName == "ollama" || provName == "llamacpp" || provName == "cluster" || provName == "abliteration"
}

/*
BuildAnswerVerifier composes the post-answer advisory layer from operator flags,
shared by the HTTP server and the CLI so both wire identical behavior. It returns
nil when nothing is enabled (zero behavior change). The NVD gate verifies cited
CVEs against the primary source; the cross-provider validator critiques vuln-
research answers with independent models (OpenAI via API, Anthropic via the Claude
Code CLI / Max subscription). Reviewers self-omit when their backend is absent
(no OPENAI_API_KEY, no claude binary), so the layer degrades gracefully.
*/
func BuildAnswerVerifier(cfg *config.Config, verifyCVE, validateVuln bool, logger *slog.Logger) agent.Verifier {
	if logger == nil {
		logger = slog.Default()
	}
	var verifiers []agent.Verifier

	if verifyCVE {
		verifiers = append(verifiers, verify.NewNVDVerifier(verify.WithAPIKey(os.Getenv("NVD_API_KEY"))))
		logger.Info("cve verification: enabled", "source", "nvd", "api_key", os.Getenv("NVD_API_KEY") != "")
	}

	if validateVuln {
		var reviewers []validate.Reviewer
		var oa config.ProviderConfig
		if cfg != nil {
			oa = cfg.Providers["openai"]
		}
		if r := validate.NewOpenAIReviewer(oa.APIKey, oa.BaseURL, oa.Model); r != nil {
			reviewers = append(reviewers, r)
		}
		if r := validate.NewClaudeCLIReviewer(""); r != nil {
			reviewers = append(reviewers, r)
		}
		if v := validate.NewCrossProviderValidator(reviewers); v != nil {
			verifiers = append(verifiers, v)
			names := make([]string, 0, len(reviewers))
			for _, r := range reviewers {
				names = append(names, r.Name())
			}
			logger.Info("cross-provider validation: enabled", "reviewers", strings.Join(names, ","))
		} else {
			logger.Warn("cross-provider validation requested but no reviewer is available (need OPENAI_API_KEY and/or the claude CLI)")
		}
	}

	return agent.NewMultiVerifier(verifiers...)
}

/*
newAgent builds a fresh agent.Agent for one request using the provider already
configured under name. Each request gets its own Agent + Session so concurrent
streams cannot interfere.
*/
func (s *Server) newAgent(name string) (*agent.Agent, error) {
	prov, ok := s.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not available", name)
	}
	/*
		Tools are rebuilt per request from the current workspace so a folder
		opened in the UI takes effect immediately and file_write/file_edit appear
		only while a workspace is set.
	*/
	var execOpts []builtin.ContainedExecOption
	if s.execImageDigest != "" {
		execOpts = append(execOpts, builtin.WithExpectedImageDigest(s.execImageDigest))
	}
	reg := builtin.NewDefaultRegistryWithExec(s.getWorkspace(), s.allowExec, execOpts...)
	a := agent.New(prov, reg, s.cfg.Agent.SystemPrompt, s.cfg.Agent.MaxIterations, s.logger)
	ragOn := s.rag != nil && !s.disableRAG
	if ragOn {
		a.RAG = s.rag
		/*
			Expose retrieval as a tool so an agentic-RAG turn can plan its own
			searches. Registered only when RAG is live; without it agentic mode has
			nothing to call, so the default below also gates on ragOn.
		*/
		if err := reg.Register(builtin.NewRAGSearchTool(s.rag)); err != nil {
			return nil, fmt.Errorf("register rag_search: %w", err)
		}
	}
	a.Agentic = s.agentic && ragOn
	a.Verifier = s.answerVerifier
	return a, nil
}

/* getWorkspace returns the folder the agentic file tools are currently scoped to. */
func (s *Server) getWorkspace() string {
	s.workspaceMu.RLock()
	defer s.workspaceMu.RUnlock()
	return s.workspace
}

/* setWorkspace replaces the active workspace folder (empty = read-only). */
func (s *Server) setWorkspace(dir string) {
	s.workspaceMu.Lock()
	s.workspace = dir
	s.workspaceMu.Unlock()
}

/*
splitModelID extracts the provider segment from "provider/model" strings,
falling back to the configured default provider for bare model names.
*/
func (s *Server) splitModelID(modelID string) (provider, model string) {
	if idx := strings.Index(modelID, "/"); idx > 0 {
		return modelID[:idx], modelID[idx+1:]
	}
	return s.cfg.DefaultProvider, modelID
}

/*
resolveProviderModel maps a request's model id to a registered provider. When the
named provider exists it is used verbatim. When it does not — e.g. an existing
conversation tagged "ollama/<model>" is continued after the server was restarted
in cluster mode, where only the "cluster" provider is registered — the request is
remapped to an available provider and the model id is cleared so that provider
serves its own default model. This keeps continuing an existing chat from ever
hard-failing across a provider/mode switch. ok is false only when no provider is
registered at all.
*/
func (s *Server) resolveProviderModel(reqModel string) (provName, modelID string, ok bool) {
	provName, modelID = s.splitModelID(reqModel)
	if _, registered := s.providers[provName]; registered {
		return provName, modelID, true
	}
	fallback, found := s.fallbackProvider()
	if !found {
		return "", "", false
	}
	s.logger.Warn("requested provider not registered; remapping to an available provider",
		"requested", provName, "fallback", fallback)
	return fallback, "", true
}

/*
fallbackProvider returns a stable, registered provider to absorb a request whose
named provider is unavailable: the cluster provider first (in cluster mode it is
the single catch-all), then the configured default, then the lexicographically
first registered provider so the choice is deterministic.
*/
func (s *Server) fallbackProvider() (string, bool) {
	if _, ok := s.providers["cluster"]; ok {
		return "cluster", true
	}
	if d := s.cfg.DefaultProvider; d != "" {
		if _, ok := s.providers[d]; ok {
			return d, true
		}
	}
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	if len(names) == 0 {
		return "", false
	}
	sort.Strings(names)
	return names[0], true
}
