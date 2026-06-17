/*
Command agent is the CLI entrypoint. It parses flags, loads configuration,
wires a llm.Provider through to an agent.Agent, and either answers a single
--prompt or reads lines from stdin in interactive mode.

Provider packages are imported with a blank identifier so their init()
functions register themselves with the llm registry.
*/
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/context7"
	"github.com/ikarolaborda/agent-smith/internal/dataset"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/abliteration"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
	"github.com/ikarolaborda/agent-smith/internal/logging"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/server"
	"github.com/ikarolaborda/agent-smith/internal/tools"
	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
	"github.com/ikarolaborda/agent-smith/internal/web"
)

/* flags groups the CLI flag values so we can pass them around as one value. */
type flags struct {
	configPath   string
	provider     string
	model        string
	prompt       string
	stream       bool
	serve        bool
	addr         string
	ingest       bool
	collection   string
	source       string
	embedder     string
	embedModel   string
	ragDir       string
	disableRAG   bool
	disableWeb   bool
	disableC7    bool
	clusterCfg   string
	workspace    string
	ragMaxChunks int
	buildDataset bool
	datasetSrc   string
	datasetOut   string
	verifyCVE    bool
	validateVuln bool
}

func main() {
	f := parseFlags()
	if err := run(f); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
}

/* parseFlags reads CLI flags. */
func parseFlags() flags {
	var f flags
	flag.StringVar(&f.configPath, "config", "configs/config.example.yaml", "path to YAML config file")
	flag.StringVar(&f.provider, "provider", "", "override default provider (openai, anthropic, ollama)")
	flag.StringVar(&f.model, "model", "", "override provider model")
	flag.StringVar(&f.prompt, "prompt", "", "single-shot prompt; if empty, read lines from stdin")
	flag.BoolVar(&f.stream, "stream", false, "stream the assistant response incrementally")
	flag.BoolVar(&f.serve, "serve", false, "start the HTTP+SSE server and serve the embedded React UI instead of the stdin loop")
	flag.StringVar(&f.addr, "addr", ":9090", "address to bind when --serve is set")
	flag.BoolVar(&f.ingest, "ingest", false, "ingest markdown docs into a RAG collection and exit")
	flag.BoolVar(&f.buildDataset, "build-dataset", false, "generate a Chat-SFT JSONL dataset from a folder of source files via the selected provider, then exit")
	flag.StringVar(&f.datasetSrc, "dataset-source", "", "directory of .md/.txt source files when --build-dataset is set")
	flag.StringVar(&f.datasetOut, "dataset-out", "dataset.jsonl", "output JSONL path when --build-dataset is set")
	flag.StringVar(&f.collection, "collection", "", "collection name when --ingest is set")
	flag.StringVar(&f.source, "source", "", "directory of .md files to ingest")
	flag.StringVar(&f.embedder, "embedder", "ollama", "embedder provider: openai | ollama")
	flag.StringVar(&f.embedModel, "embed-model", "", "embedding model override (defaults: text-embedding-3-small / nomic-embed-text)")
	flag.StringVar(&f.ragDir, "rag-dir", "data/rag/collections", "directory holding RAG collection JSON files")
	flag.BoolVar(&f.disableRAG, "no-rag", false, "disable RAG augmentation (still loads collections for /v1/rag endpoints)")
	flag.BoolVar(&f.disableWeb, "no-web-search", false, "operator kill switch for the web-grounding gate (overrides all per-request flags)")
	flag.BoolVar(&f.disableC7, "no-context7", false, "operator kill switch for Context7 documentation augmentation (otherwise on when CONTEXT7_API_KEY is set)")
	flag.StringVar(&f.clusterCfg, "cluster-config", "", "path to a cluster YAML config; enables clusterized inference (exo/MLX/llama.cpp RPC) with local fallback")
	flag.StringVar(&f.workspace, "workspace", "", "enable agentic project work: directory the agent may modify via file_write/file_edit (sandboxed). Unset = read-only.")
	flag.IntVar(&f.ragMaxChunks, "rag-max-chunks", 0, "override how many RAG chunks are injected per request (0 = default 4). Raise for large-context cluster models to improve grounding.")
	flag.BoolVar(&f.verifyCVE, "verify-cve", false, "verify CVE identifiers in answers against the NIST NVD primary source and append a non-destructive advisory note (network egress; reads NVD_API_KEY from env if set)")
	flag.BoolVar(&f.validateVuln, "validate-vuln", false, "cross-validate vulnerability-research answers against independent models (OpenAI via API; Anthropic via the Claude Code CLI / Max subscription) and append a non-authoritative advisory (network egress; drives the Max subscription programmatically)")
	flag.Parse()
	return f
}

/*
run wires the application together. It loads config (including the .env
adjacent to the config file), builds the active provider, constructs the
Agent, and dispatches to either runOnce or runInteractive depending on
whether --prompt is set.
*/
func run(f flags) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load(f.configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := logging.New(logging.Options{Format: cfg.Logging.Format, Level: cfg.Logging.Level})
	logger.Info("agent starting", "config", f.configPath, "provider", chooseProviderName(cfg, f), "stream", f.stream, "serve", f.serve, "ingest", f.ingest)

	if f.ingest {
		return runIngest(ctx, cfg, f, logger)
	}

	if f.buildDataset {
		return runBuildDataset(ctx, cfg, f, logger)
	}

	if f.serve {
		return runServe(ctx, cfg, f, logger)
	}

	var provider llm.Provider
	if f.clusterCfg != "" {
		cp, err := buildClusterProvider(ctx, cfg, f, logger)
		if err != nil {
			return fmt.Errorf("build cluster provider: %w", err)
		}
		defer func() { _ = cp.Close(context.Background()) }()
		provider = cp
	} else {
		provider, err = buildProvider(cfg, f)
		if err != nil {
			return fmt.Errorf("build provider: %w", err)
		}
	}

	a := agent.New(provider, buildTools(f, logger), cfg.Agent.SystemPrompt, cfg.Agent.MaxIterations, logger)
	a.Verifier = server.BuildAnswerVerifier(cfg, f.verifyCVE, f.validateVuln, logger)

	if f.prompt != "" {
		return runOnce(ctx, a, f.prompt)
	}
	return runInteractive(ctx, a, os.Stdin, os.Stdout)
}

/*
buildTools assembles the CLI agent's tool registry. It delegates to the shared
builtin.NewDefaultRegistry so terminal and web expose identical capabilities, and
logs when --workspace enables the mutating file tools.
*/
func buildTools(f flags, logger *slog.Logger) *tools.Registry {
	if f.workspace != "" {
		logger.Info("workspace: agentic file mutation enabled", "root", f.workspace)
	}
	return builtin.NewDefaultRegistry(f.workspace)
}

/*
runServe wires the embedded HTTP server with all configured providers and the
default tool registry, then blocks until ctx is cancelled.
*/
func runServe(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	if f.workspace != "" {
		logger.Info("workspace: agentic file mutation enabled", "root", f.workspace)
	}

	embedders, err := buildEmbedders(cfg, f)
	if err != nil {
		logger.Warn("rag: embedders not initialized", "err", err)
	}
	ragSvc, err := rag.NewService(f.ragDir, embedders, logger)
	if err != nil {
		logger.Warn("rag: service not started", "err", err)
		ragSvc = nil
	}
	/*
		A large-context cluster model grounds better with more retrieved chunks;
		the threshold still gates relevance, so this widens evidence without
		injecting noise. Left at the default for small single-node models.
	*/
	if ragSvc != nil && f.ragMaxChunks > 0 {
		/*
			Bound the override: beyond ~64 chunks weak ranking can bury salient
			evidence among near-duplicates (dilution), which hurts grounding
			rather than helping. Recommended range 8–16 for the 72B cluster.
		*/
		if f.ragMaxChunks > 64 {
			f.ragMaxChunks = 64
			logger.Warn("rag: --rag-max-chunks clamped to 64 (higher dilutes salience)")
		}
		ragSvc.MaxChunks = f.ragMaxChunks
		/*
			The byte cap would otherwise throttle the extra chunks, so scale it
			with the chunk count (≈2KB/chunk) — only widening, never shrinking
			the default.
		*/
		if want := f.ragMaxChunks * 2000; want > ragSvc.MaxBytes {
			ragSvc.MaxBytes = want
		}
		logger.Info("rag: grounding widened", "max_chunks", ragSvc.MaxChunks, "max_bytes", ragSvc.MaxBytes)
	}
	if ragSvc != nil && !f.disableWeb {
		ragSvc.WebSearch = web.NewDDGSearcher()
		logger.Info("web grounding: enabled", "backend", "ddg")
	}
	/*
		Context7 documentation augmentation is always-on when an API key is
		present (and not killed by --no-context7), so every model — local ones
		included — transparently gets current library docs. The key lives in the
		environment (loaded from .env adjacent to the config); CONTEXT7_BASE_URL
		overrides the endpoint root if the API path ever changes.
	*/
	if ragSvc != nil && !f.disableC7 {
		if key := os.Getenv("CONTEXT7_API_KEY"); key != "" {
			ragSvc.Context7 = context7.New(key, os.Getenv("CONTEXT7_BASE_URL"))
			logger.Info("context7 augmentation: enabled")
		} else {
			logger.Info("context7 augmentation: disabled", "reason", "CONTEXT7_API_KEY not set")
		}
	}

	/*
		In cluster mode the server is wired with the single "cluster" provider:
		every chat request is routed through the cluster control plane (exo /
		MLX / llama.cpp RPC) with the local runner as fallback. Non-cluster
		serve mode is unchanged and builds providers from config as before.
	*/
	var injected map[string]llm.Provider
	if f.clusterCfg != "" {
		cp, err := buildClusterProvider(ctx, cfg, f, logger)
		if err != nil {
			return fmt.Errorf("build cluster provider: %w", err)
		}
		defer func() { _ = cp.Close(context.Background()) }()
		injected = map[string]llm.Provider{cp.Name(): cp}
	}

	srv, err := server.New(server.Options{
		Addr:             f.addr,
		Config:           cfg,
		Workspace:        f.workspace,
		Logger:           logger,
		RAG:              ragSvc,
		DisableRAG:       f.disableRAG,
		WebSearchEnabled: !f.disableWeb,
		VerifyCVE:        f.verifyCVE,
		ValidateVuln:     f.validateVuln,
		Providers:        injected,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	return srv.ListenAndServe(ctx)
}

/*
buildClusterProvider loads the cluster YAML and constructs the cluster provider.
The local single-node fallback is the provider built from the existing config
(best-effort: a missing credential leaves the fallback nil, which is only fatal
when runtime.strict_cluster is set).
*/
func buildClusterProvider(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) (*cluster.Provider, error) {
	ccfg, err := cluster.LoadClusterConfig(f.clusterCfg)
	if err != nil {
		return nil, err
	}
	local, err := buildProvider(cfg, f)
	if err != nil {
		logger.Warn("cluster: local fallback provider unavailable", "err", err)
		local = nil
	}
	return cluster.New(ctx, ccfg, local, logger)
}

/*
runBuildDataset turns a folder of .md/.txt source files into an OpenAI chat
fine-tuning JSONL dataset: each file becomes one grounded Chat-SFT example
generated by the selected provider (e.g. abliteration) at low temperature. It is
the native, zero-dependency equivalent of abliteration.ai's console dataset
product, so it stays inside the offline-first single binary.
*/
func runBuildDataset(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	if f.datasetSrc == "" {
		return errors.New("--build-dataset requires --dataset-source <dir>")
	}
	provider, err := buildProvider(cfg, f)
	if err != nil {
		return fmt.Errorf("build provider: %w", err)
	}

	var items []dataset.SourceItem
	walkErr := filepath.WalkDir(f.datasetSrc, func(p string, d os.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".md" && ext != ".txt" {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(f.datasetSrc, p)
		items = append(items, dataset.SourceItem{ID: rel, Content: string(b)})
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("scan dataset source: %w", walkErr)
	}
	if len(items) == 0 {
		return fmt.Errorf("no .md/.txt source files under %s", f.datasetSrc)
	}

	logger.Info("build-dataset: generating", "items", len(items), "provider", chooseProviderName(cfg, f), "out", f.datasetOut)
	records, skipped, err := dataset.Build(ctx, provider, items, dataset.Options{})
	if err != nil {
		return fmt.Errorf("build dataset: %w", err)
	}

	out, err := os.Create(f.datasetOut)
	if err != nil {
		return fmt.Errorf("create %s: %w", f.datasetOut, err)
	}
	defer func() { _ = out.Close() }()
	if err := dataset.WriteJSONL(out, records); err != nil {
		return fmt.Errorf("write jsonl: %w", err)
	}
	logger.Info("build-dataset: done", "records", len(records), "skipped", skipped, "out", f.datasetOut)
	return nil
}

/*
runIngest is the offline path: it builds the requested embedder, opens the
RAG store, and ingests every .md under --source into --collection. The
binary exits when ingest completes.
*/
func runIngest(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	if f.collection == "" || f.source == "" {
		return errors.New("--ingest requires --collection and --source")
	}
	embedder, err := buildSingleEmbedder(cfg, f)
	if err != nil {
		return fmt.Errorf("build embedder: %w", err)
	}
	embedders := map[string]llm.Embedder{embedder.Identity(): embedder}
	svc, err := rag.NewService(f.ragDir, embedders, logger)
	if err != nil {
		return fmt.Errorf("rag service: %w", err)
	}
	col, err := svc.Ingest(ctx, f.collection, f.source, embedder, rag.DefaultChunkOptions)
	if err != nil {
		return fmt.Errorf("ingest: %w", err)
	}
	logger.Info("ingest complete", "collection", col.Name, "chunks", len(col.Chunks), "embedder", col.EmbedderID, "dim", col.Dim)
	return nil
}

/*
buildEmbedders constructs every embedder for which the relevant provider is
configured (and credentialed). Returns an empty map when no embedder can be
built; the server treats RAG as a soft feature.
*/
func buildEmbedders(cfg *config.Config, f flags) (map[string]llm.Embedder, error) {
	out := map[string]llm.Embedder{}
	for name := range cfg.Providers {
		f2 := f
		f2.embedder = name
		e, err := buildSingleEmbedder(cfg, f2)
		if err != nil {
			continue
		}
		out[e.Identity()] = e
	}
	if len(out) == 0 {
		return out, errors.New("no embedders could be built from configured providers")
	}
	return out, nil
}

/* buildSingleEmbedder constructs one embedder by name (openai or ollama). */
func buildSingleEmbedder(cfg *config.Config, f flags) (llm.Embedder, error) {
	name := f.embedder
	if name == "" {
		name = "ollama"
	}
	pcfg, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q has no config block", name)
	}
	switch name {
	case "openai":
		if pcfg.APIKey == "" {
			return nil, fmt.Errorf("openai embed: missing api_key")
		}
		c, err := openai.New(openai.Config{APIKey: pcfg.APIKey, BaseURL: pcfg.BaseURL, Model: pcfg.Model})
		if err != nil {
			return nil, err
		}
		return openai.NewEmbedder(c, openai.EmbedConfig{Model: f.embedModel})
	case "ollama":
		c, err := ollama.New(ollama.Config{BaseURL: pcfg.BaseURL, Model: pcfg.Model})
		if err != nil {
			return nil, err
		}
		return ollama.NewEmbedder(c, ollama.EmbedConfig{Model: f.embedModel})
	default:
		return nil, fmt.Errorf("unknown embedder provider %q", name)
	}
}

/* chooseProviderName picks the active provider name from the flag or the config default. */
func chooseProviderName(cfg *config.Config, f flags) string {
	if f.provider != "" {
		return f.provider
	}
	return cfg.DefaultProvider
}

/*
buildProvider resolves the active provider name, locates its config block,
applies the --model override if set, and asks the llm registry to construct
the concrete client.
*/
func buildProvider(cfg *config.Config, f flags) (llm.Provider, error) {
	name := chooseProviderName(cfg, f)
	if name == "" {
		return nil, errors.New("no provider selected and no default in config")
	}
	pcfg, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q has no config block", name)
	}
	if f.model != "" {
		pcfg.Model = f.model
	}

	switch name {
	case "openai":
		return llm.New(name, openai.Config{APIKey: pcfg.APIKey, BaseURL: pcfg.BaseURL, Model: pcfg.Model})
	case "abliteration":
		return llm.New(name, abliteration.Config{APIKey: pcfg.APIKey, BaseURL: pcfg.BaseURL, Model: pcfg.Model})
	case "anthropic":
		return llm.New(name, anthropic.Config{APIKey: pcfg.APIKey, BaseURL: pcfg.BaseURL, Model: pcfg.Model})
	case "ollama":
		return llm.New(name, ollama.Config{BaseURL: pcfg.BaseURL, Model: pcfg.Model})
	default:
		return nil, fmt.Errorf("unknown provider %q (registered: %v)", name, llm.Names())
	}
}

/* runOnce answers a single prompt and writes the result to stdout. */
func runOnce(ctx context.Context, a *agent.Agent, prompt string) error {
	out, err := a.Run(ctx, agent.NewSession(), prompt)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

/*
runInteractive reads one prompt per line from r and writes the assistant
response to w. The session is reused so the conversation builds up across
turns.
*/
func runInteractive(ctx context.Context, a *agent.Agent, r io.Reader, w io.Writer) error {
	session := agent.NewSession()
	scanner := bufio.NewScanner(r)
	for {
		fmt.Fprint(w, "> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		reply, err := a.Run(ctx, session, line)
		if err != nil {
			fmt.Fprintln(w, "error:", err)
			continue
		}
		fmt.Fprintln(w, reply)
	}
	return scanner.Err()
}
