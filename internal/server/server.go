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
	"strings"
	"sync"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/tools"
)

/* Options carries the configuration needed to build a Server. */
type Options struct {
	Addr   string
	Config *config.Config
	Tools  *tools.Registry
	Logger *slog.Logger
	/*
		Providers, when non-nil, replaces the providers New would build from
		Config.Providers. Used by tests to inject fake provider clients;
		production callers should leave it nil.
	*/
	Providers      map[string]llm.Provider
	AllowedOrigins []string
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	/* RAG, when non-nil, is the retrieval service consulted by chat handlers. */
	RAG *rag.Service
	/* DisableRAG, when true, suppresses augmentation regardless of RAG presence. */
	DisableRAG bool
	/*
		WebSearchEnabled is the operator-level kill switch for the web
		grounding gate. When false, the chat handler never injects web
		results regardless of per-request or provider-default flags. When
		true (default), Ollama requests get web grounding ON by default
		and cloud providers OFF; the request body's web_search field can
		flip either way.
	*/
	WebSearchEnabled bool
}

/*
Server bundles the HTTP listener, route mux, configured providers, and tool
registry. Each chat request gets its own agent.Agent instance built from an
already-constructed provider, so concurrent streams do not share mutable
state.
*/
type Server struct {
	addr             string
	cfg              *config.Config
	tools            *tools.Registry
	logger           *slog.Logger
	providers        map[string]llm.Provider
	models           []modelEntry
	mux              *http.ServeMux
	allowedOrigins   map[string]struct{}
	readTimeout      time.Duration
	writeTimeout     time.Duration
	rag              *rag.Service
	disableRAG       bool
	webSearchEnabled bool

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
		opts.Addr = ":9090"
	}

	s := &Server{
		addr:             opts.Addr,
		cfg:              opts.Config,
		tools:            opts.Tools,
		logger:           logger,
		providers:        map[string]llm.Provider{},
		mux:              http.NewServeMux(),
		allowedOrigins:   map[string]struct{}{},
		readTimeout:      opts.ReadTimeout,
		writeTimeout:     opts.WriteTimeout,
		rag:              opts.RAG,
		disableRAG:       opts.DisableRAG,
		webSearchEnabled: opts.WebSearchEnabled,
	}
	for _, o := range opts.AllowedOrigins {
		s.allowedOrigins[o] = struct{}{}
	}

	created := time.Now().Unix()
	if opts.Providers != nil {
		for name, prov := range opts.Providers {
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
				SupportsVision: cloudModelSupportsVision(name, model),
			})
		}
	} else {
		for name, p := range opts.Config.Providers {
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
	s.mux.HandleFunc("/v1/models", s.handleModels)
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
shouldWebSearch applies the precedence rules: operator kill switch first,
then per-request override, then provider default (true for Ollama only).
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
		Default web grounding ON for local-model providers. The cluster provider
		runs local 70B/72B models (exo / MLX / llama.cpp) exactly like Ollama,
		so it needs the same hallucination-suppressing grounding by default. The
		operator kill switch (--no-web-search) and the per-request override both
		still take precedence over this default.
	*/
	return provName == "ollama" || provName == "cluster"
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
	reg := s.tools
	if reg == nil {
		reg = tools.NewRegistry()
	}
	a := agent.New(prov, reg, s.cfg.Agent.SystemPrompt, s.cfg.Agent.MaxIterations, s.logger)
	if s.rag != nil && !s.disableRAG {
		a.RAG = s.rag
	}
	return a, nil
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
