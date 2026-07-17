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
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/context7"
	"github.com/ikarolaborda/agent-smith/internal/dataset"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/abliteration"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
	"github.com/ikarolaborda/agent-smith/internal/logging"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/refine"
	"github.com/ikarolaborda/agent-smith/internal/server"
	"github.com/ikarolaborda/agent-smith/internal/tools"
	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
	"github.com/ikarolaborda/agent-smith/internal/web"
)

/* flags groups the CLI flag values so we can pass them around as one value. */
type flags struct {
	configPath      string
	provider        string
	model           string
	prompt          string
	stream          bool
	serve           bool
	addr            string
	ingest          bool
	collection      string
	source          string
	embedder        string
	embedModel      string
	ragDir          string
	disableRAG      bool
	disableWeb      bool
	disableC7       bool
	clusterCfg      string
	workspace       string
	ragMaxChunks    int
	buildDataset    bool
	datasetSrc      string
	datasetOut      string
	verifyCVE       bool
	validateVuln    bool
	allowExec       bool
	execImageDigest string
	refineLoop      bool
	refineIters     int
	refineTO        time.Duration
	pull            string
	inspectModel    string
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
	flag.StringVar(&f.provider, "provider", "", "override default provider (openai, anthropic, ollama, llamacpp, abliteration)")
	flag.StringVar(&f.model, "model", "", "override provider model")
	flag.StringVar(&f.prompt, "prompt", "", "single-shot prompt; if empty, read lines from stdin")
	flag.BoolVar(&f.stream, "stream", false, "stream the assistant response incrementally")
	flag.BoolVar(&f.serve, "serve", false, "start the HTTP+SSE server and serve the embedded React UI instead of the stdin loop")
	flag.StringVar(&f.addr, "addr", "127.0.0.1:9090", "address to bind when --serve is set (set :9090 explicitly to expose on every interface)")
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
	flag.BoolVar(&f.allowExec, "allow-exec", false, "enable the OPT-IN container-contained execution tool (ADR 0003): the agent may run fixed apparatus operations (fuzz/reproduce/triage) inside an ephemeral, network-isolated, read-only Docker container mounting --workspace. OFF by default; requires --workspace and Docker. Each run is audited.")
	flag.StringVar(&f.execImageDigest, "exec-image-digest", "", "pin the contained-exec apparatus image to an exact local image ID (sha256:<hex>, from `docker images --no-trunc --quiet php74-asan`). With --pull=never this makes image resolution fail closed on any other content, defeating a local re-tag. Empty = resolve by tag (unpinned).")
	flag.BoolVar(&f.refineLoop, "refine-loop", false, "OPT-IN single-shot refinement loop (requires --prompt + OpenAI judge): regenerate the answer with the gpt-5.x judge's critique until it is judged USABLE (grounded, feasible, honestly scoped) or the iteration budget is exhausted. Anti-fabrication: an honest negative passes; the loop never fakes a pass. CLI-only.")
	flag.IntVar(&f.refineIters, "refine-max-iters", refine.DefaultMaxIters, "maximum refinement iterations when --refine-loop is set")
	flag.DurationVar(&f.refineTO, "refine-timeout", refine.DefaultRoundTimeout, "per-round timeout (generate+judge) when --refine-loop is set")
	flag.StringVar(&f.pull, "pull", "", "download a GGUF model from Hugging Face and exit (e.g. hf.co/ggml-org/gemma-3-1b-it-GGUF:Q4_K_M). Uses the llamacpp provider's models_dir/hf_token when configured.")
	flag.StringVar(&f.inspectModel, "inspect-model", "", "resolve a GGUF artifact manifest, inspect this host, print the fit report as JSON, and exit without downloading model data")
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

	if f.inspectModel != "" {
		return runInspectModel(ctx, cfg, f, logger)
	}

	if f.pull != "" {
		return runPull(ctx, cfg, f, logger)
	}

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
		provider, err = buildProvider(ctx, cfg, f, logger)
		if err != nil {
			return fmt.Errorf("build provider: %w", err)
		}
		/*
			A self-managing provider (llamacpp supervises a llama-server child)
			exposes Close; stop the subprocess on exit so no server is orphaned.
		*/
		if c, ok := provider.(interface{ Close(context.Context) error }); ok {
			defer func() { _ = c.Close(context.Background()) }()
		}
	}

	a := agent.New(provider, buildTools(f, logger), cfg.Agent.SystemPrompt, cfg.Agent.MaxIterations, logger)
	a.Verifier = server.BuildAnswerVerifier(cfg, f.verifyCVE, f.validateVuln, logger)
	/*
		Grounding belongs above the provider boundary. Attach it to the ordinary
		CLI paths too, so switching from the web UI to --prompt/interactive mode
		does not silently drop the knowledge layer for the exact same model.
	*/
	if !f.disableRAG {
		ragSvc, ragErr := buildRAG(cfg, &f, logger)
		if ragErr != nil {
			return fmt.Errorf("initialize required knowledge layer: %w", ragErr)
		}
		a.RAG = ragSvc
		a.WebSearch = !f.disableWeb && isLocalProvider(provider.Name())
	}

	if f.refineLoop {
		if f.prompt == "" {
			return errors.New("--refine-loop requires --prompt")
		}
		return runRefine(ctx, cfg, f, a, logger)
	}

	if f.prompt != "" {
		return runOnce(ctx, a, f.prompt)
	}
	return runInteractive(ctx, a, os.Stdin, os.Stdout)
}

/* isLocalProvider selects providers whose CLI web-grounding default is enabled. */
func isLocalProvider(name string) bool {
	switch name {
	case "ollama", "llamacpp", "cluster", "abliteration":
		return true
	default:
		return false
	}
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
	logExecBanner(f, logger)
	var execOpts []builtin.ContainedExecOption
	if f.execImageDigest != "" {
		execOpts = append(execOpts, builtin.WithExpectedImageDigest(f.execImageDigest))
		logger.Info("exec: apparatus image pinned by digest", "digest", f.execImageDigest)
	}
	return builtin.NewDefaultRegistryWithExec(f.workspace, f.allowExec, execOpts...)
}

/*
logExecBanner emits the high-visibility startup banner ADR 0003 requires when
contained execution is enabled, and warns about the misconfigurations that make
the gate a no-op (no workspace, or Docker absent).
*/
func logExecBanner(f flags, logger *slog.Logger) {
	if !f.allowExec {
		return
	}
	if f.workspace == "" {
		logger.Warn("exec: --allow-exec set but no --workspace; the contained run tool is NOT registered")
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		logger.Warn("exec: --allow-exec set but docker not found on PATH; contained runs will fail", "err", err)
	}
	logger.Warn("exec: CONTAINED EXECUTION ENABLED — agent may run fuzz/reproduce/triage in an ephemeral, network-isolated, read-only Docker container", "workspace", f.workspace)
}

/*
buildRAG constructs the RAG service with the same grounding posture used by the
server: optional chunk-count widening for large-context cluster models, web
grounding, and Context7 documentation augmentation. It is shared by CLI and
server paths so both ground identically. Failure to load the required embedded
knowledge layer is returned to the caller and stops startup.
*/
func buildRAG(cfg *config.Config, f *flags, logger *slog.Logger) (*rag.Service, error) {
	embedders, err := buildEmbedders(cfg, *f)
	if err != nil {
		logger.Warn("rag: embedders not initialized", "err", err)
	}
	ragSvc, err := rag.NewService(f.ragDir, embedders, logger)
	if err != nil {
		return nil, err
	}
	/*
		Memory may contain private project/profile facts. Bind it to the
		operator-selected --embedder backend instead of allowing the first write
		to choose nondeterministically from every configured provider.
	*/
	memoryEmbedderID, memoryErr := requestedMemoryEmbedderID(*f)
	if memoryErr != nil {
		return nil, memoryErr
	}
	ragSvc.MemoryEmbedderID = memoryEmbedderID
	if _, ok := embedders[memoryEmbedderID]; !ok {
		logger.Warn("rag: preferred memory embedder unavailable; memory writes are disabled until it is configured",
			"embedder", memoryEmbedderID)
	}
	if f.ragMaxChunks > 0 {
		if f.ragMaxChunks > 64 {
			f.ragMaxChunks = 64
			logger.Warn("rag: --rag-max-chunks clamped to 64 (higher dilutes salience)")
		}
		ragSvc.MaxChunks = f.ragMaxChunks
		if want := f.ragMaxChunks * 2000; want > ragSvc.MaxBytes {
			ragSvc.MaxBytes = want
		}
		logger.Info("rag: grounding widened", "max_chunks", ragSvc.MaxChunks, "max_bytes", ragSvc.MaxBytes)
	}
	if !f.disableWeb {
		ragSvc.WebSearch = web.NewDDGSearcher()
		logger.Info("web grounding: enabled", "backend", "ddg")
	}
	if !f.disableC7 {
		if key := os.Getenv("CONTEXT7_API_KEY"); key != "" {
			ragSvc.Context7 = context7.New(key, os.Getenv("CONTEXT7_BASE_URL"))
			logger.Info("context7 augmentation: enabled")
		} else {
			logger.Info("context7 augmentation: disabled", "reason", "CONTEXT7_API_KEY not set")
		}
	}
	return ragSvc, nil
}

/*
runServe wires the embedded HTTP server with all configured providers and the
default tool registry, then blocks until ctx is cancelled.
*/
func runServe(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	serverCfg, err := configForServe(cfg, f)
	if err != nil {
		return err
	}
	if f.workspace != "" {
		logger.Info("workspace: agentic file mutation enabled", "root", f.workspace)
	}
	logExecBanner(f, logger)

	ragSvc, err := buildRAG(serverCfg, &f, logger)
	if err != nil {
		return fmt.Errorf("initialize required knowledge layer: %w", err)
	}

	/*
		In cluster mode the server is wired with the single "cluster" provider:
		every chat request is routed through the cluster control plane (exo /
		MLX / llama.cpp RPC) with the local runner as fallback. Non-cluster
		serve mode is unchanged and builds providers from config as before.
	*/
	var injected map[string]llm.Provider
	if f.clusterCfg != "" {
		cp, err := buildClusterProvider(ctx, serverCfg, f, logger)
		if err != nil {
			return fmt.Errorf("build cluster provider: %w", err)
		}
		defer func() { _ = cp.Close(context.Background()) }()
		injected = map[string]llm.Provider{cp.Name(): cp}
	}

	/*
		The llamacpp provider must download+launch a llama-server before the
		server can route to it, which the server's own per-config provider
		builder cannot do (it has no process lifecycle). Build it here, own its
		shutdown, and hand it to the server as an additive extra provider so it
		coexists with the config-built cloud/ollama providers.
	*/
	var extra map[string]llm.Provider
	if p, ok := serverCfg.Providers["llamacpp"]; ok && p.LlamaCpp != nil && f.clusterCfg == "" && serverCfg.DefaultProvider == "llamacpp" {
		prov, err := buildLlamaCppProvider(ctx, p, logger)
		if err != nil {
			return fmt.Errorf("build llamacpp provider: %w", err)
		}
		if c, ok := prov.(interface{ Close(context.Context) error }); ok {
			defer func() { _ = c.Close(context.Background()) }()
		}
		extra = map[string]llm.Provider{prov.Name(): prov}
	}

	srv, err := server.New(server.Options{
		Addr:             f.addr,
		Config:           serverCfg,
		Workspace:        f.workspace,
		AllowExec:        f.allowExec,
		ExecImageDigest:  f.execImageDigest,
		Logger:           logger,
		RAG:              ragSvc,
		DisableRAG:       f.disableRAG,
		WebSearchEnabled: !f.disableWeb,
		VerifyCVE:        f.verifyCVE,
		ValidateVuln:     f.validateVuln,
		Providers:        injected,
		ExtraProviders:   extra,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	return srv.ListenAndServe(ctx)
}

/* configForServe makes CLI provider/model overrides authoritative for empty-model API requests and the UI default. */
func configForServe(cfg *config.Config, f flags) (*config.Config, error) {
	if cfg == nil {
		return nil, errors.New("serve: nil config")
	}
	selected := chooseProviderName(cfg, f)
	provider, ok := cfg.Providers[selected]
	if !ok {
		return nil, fmt.Errorf("serve: provider %q has no config block", selected)
	}
	clone := *cfg
	clone.DefaultProvider = selected
	clone.Providers = make(map[string]config.ProviderConfig, len(cfg.Providers))
	for name, candidate := range cfg.Providers {
		clone.Providers[name] = candidate
	}
	if f.model != "" {
		provider.Model = f.model
		clone.Providers[selected] = provider
	}
	return &clone, nil
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
	local, err := buildProvider(ctx, cfg, f, logger)
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
	provider, err := buildProvider(ctx, cfg, f, logger)
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
configured (and credentialed). An empty map still permits the required embedded
lexical corpus; dense retrieval and writable memory remain unavailable.
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

/* requestedMemoryEmbedderID resolves --embedder/--embed-model without contacting a backend. */
func requestedMemoryEmbedderID(f flags) (string, error) {
	name := f.embedder
	if name == "" {
		name = "ollama"
	}
	model := f.embedModel
	switch name {
	case "openai":
		if model == "" {
			model = "text-embedding-3-small"
		}
	case "ollama":
		if model == "" {
			model = "nomic-embed-text"
		}
	default:
		return "", fmt.Errorf("unknown memory embedder provider %q", name)
	}
	return name + ":" + model, nil
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
func buildProvider(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) (llm.Provider, error) {
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
	case "llamacpp":
		return buildLlamaCppProvider(ctx, pcfg, logger)
	default:
		return nil, fmt.Errorf("unknown provider %q (registered: %v)", name, llm.Names())
	}
}

/*
buildLlamaCppProvider downloads the model if needed, starts a local llama-server,
and returns a Provider that owns the subprocess. The returned provider exposes
Close; callers must defer it so the server is stopped on exit.
*/
func buildLlamaCppProvider(ctx context.Context, pcfg config.ProviderConfig, logger *slog.Logger) (llm.Provider, error) {
	lc := pcfg.LlamaCpp
	if lc == nil {
		return nil, errors.New("llamacpp: provider selected but no llama_cpp config block")
	}

	rc := llamacpp.RuntimeConfig{
		Binary:         lc.Binary,
		ModelPath:      lc.ModelPath,
		MMProjPath:     lc.MMProjPath,
		Host:           lc.Host,
		Port:           lc.Port,
		CtxSize:        effectiveLlamaContext(lc),
		Parallel:       effectiveLlamaParallel(lc),
		GPULayers:      lc.GPULayers,
		Jinja:          lc.Jinja,
		ExtraArgs:      lc.ExtraArgs,
		StartupTimeout: time.Duration(lc.StartupTimeoutSeconds) * time.Second,
		APIKey:         pcfg.APIKey,
		Logger:         logger,
	}
	if lc.ModelPath == "" {
		if lc.Repo == "" {
			return nil, errors.New("llamacpp: set llama_cpp.model_path or llama_cpp.repo")
		}
		ref, err := llamacpp.ParseRef(lc.Repo)
		if err != nil {
			return nil, err
		}
		if lc.File != "" {
			ref.File = lc.File
		}
		if lc.Quant != "" {
			ref.Quant = lc.Quant
		}
		if lc.Revision != "" {
			ref.Revision = lc.Revision
		}
		if lc.MMProjFile != "" {
			ref.MMProjFile = lc.MMProjFile
		}
		modelsDir := lc.ModelsDir
		if modelsDir == "" {
			modelsDir = defaultModelsDir()
		}
		rc.Ref = &ref
		rc.Downloader = llamacpp.NewDownloader(modelsDir, hfToken(lc.HFToken), logger)
		rc.Downloader.ContextTokens = rc.CtxSize
		rc.Downloader.Parallel = rc.Parallel
	}

	/*
		Auto-tune when the operator has set neither gpu_layers nor ctx_size:
		detect the host (GPU/VRAM/RAM) and pick a launch profile that makes the
		most of it — full GPU offload with a generous context when the accelerator
		has room, CPU-bounded otherwise. Explicit config always wins.
	*/
	if lc.GPULayers == 0 && lc.CtxSize == 0 {
		if rec, ok := autoTuneLlama(ctx, lc, rc); ok {
			rc.GPULayers = rec.GPULayers
			rc.CtxSize = rec.CtxSize
			rc.KVCacheType = rec.KVCacheType
			if rc.Downloader != nil {
				rc.Downloader.ContextTokens = rc.CtxSize
			}
			logger.Info("llamacpp: auto-tuned for detected hardware",
				"gpu_layers", rec.GPULayers, "ctx_size", rec.CtxSize, "backend", rec.Backend,
				"kv_cache_type", rec.KVCacheType,
				"rationale", strings.Join(rec.Rationale, "; "))
		}
	}

	rt := llamacpp.NewRuntime(rc)
	if err := rt.Start(ctx); err != nil {
		return nil, err
	}
	prov, err := llamacpp.New(llamacpp.Config{Runtime: rt, Model: pcfg.Model, APIKey: pcfg.APIKey})
	if err != nil {
		_ = rt.Close(context.Background())
		return nil, err
	}
	return prov, nil
}

/*
autoTuneLlama derives a hardware-aware launch profile (GPU layers + context)
from the detected host and the model's artifact sizes. For a repo it reuses the
downloader's manifest inspection (no download); for a local model_path it stats
the file. Returns false when sizes or the host cannot be determined, so the
caller keeps its defaults.
*/
func autoTuneLlama(ctx context.Context, lc *config.LlamaCppConfig, rc llamacpp.RuntimeConfig) (llamacpp.Recommendation, bool) {
	if rc.Ref != nil && rc.Downloader != nil {
		plan, err := rc.Downloader.Inspect(ctx, *rc.Ref)
		if err != nil {
			return llamacpp.Recommendation{}, false
		}
		return llamacpp.RecommendRuntime(plan.Host, plan.Manifest.ModelBytes(), plan.Manifest.MMProjBytes(), 0), true
	}
	if lc.ModelPath != "" {
		host, err := llamacpp.SystemProfiler{}.Profile(ctx, filepath.Dir(lc.ModelPath))
		if err != nil {
			return llamacpp.Recommendation{}, false
		}
		modelBytes := statSize(lc.ModelPath)
		if modelBytes == 0 {
			return llamacpp.Recommendation{}, false
		}
		return llamacpp.RecommendRuntime(host, modelBytes, statSize(lc.MMProjPath), 0), true
	}
	return llamacpp.Recommendation{}, false
}

func statSize(path string) uint64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return uint64(info.Size())
}

/*
runPull resolves and downloads a GGUF model without starting a server, so a
model can be pre-fetched. It reuses the llamacpp provider's models_dir and token
when a llama_cpp config block is present, else uses defaults and $HF_TOKEN.
*/
func runPull(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	ref, dl, err := configuredLlamaDownload(cfg, f.pull, logger)
	if err != nil {
		return err
	}
	plan, err := dl.Inspect(ctx, ref)
	if err != nil {
		return fmt.Errorf("pull preflight: %w", err)
	}
	if err := writeJSON(os.Stdout, plan); err != nil {
		return fmt.Errorf("print pull preflight: %w", err)
	}
	if !plan.Fit.Fits {
		return &llamacpp.FitError{Report: plan.Fit}
	}
	/* Download exactly the commit that produced the displayed fit report. */
	ref.Revision = plan.Manifest.CommitSHA
	local, err := dl.EnsureArtifacts(ctx, ref)
	if err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	for _, path := range local.ModelFiles {
		fmt.Println(path)
	}
	if local.MMProj != "" {
		fmt.Println(local.MMProj)
	}
	return nil
}

/* runInspectModel performs the same metadata and live-host admission as pull, without artifact GETs. */
func runInspectModel(ctx context.Context, cfg *config.Config, f flags, logger *slog.Logger) error {
	ref, dl, err := configuredLlamaDownload(cfg, f.inspectModel, logger)
	if err != nil {
		return err
	}
	plan, err := dl.Inspect(ctx, ref)
	if err != nil {
		return fmt.Errorf("inspect model: %w", err)
	}
	if err := writeJSON(os.Stdout, plan); err != nil {
		return fmt.Errorf("print model inspection: %w", err)
	}
	if !plan.Fit.Fits {
		return &llamacpp.FitError{Report: plan.Fit}
	}
	return nil
}

/* configuredLlamaDownload applies repository selectors only to their configured repository. */
func configuredLlamaDownload(cfg *config.Config, raw string, logger *slog.Logger) (llamacpp.Ref, *llamacpp.Downloader, error) {
	ref, err := llamacpp.ParseRef(raw)
	if err != nil {
		return llamacpp.Ref{}, nil, err
	}
	modelsDir := defaultModelsDir()
	token := hfToken("")
	ctxSize := 4096
	parallel := 1
	if provider, ok := cfg.Providers["llamacpp"]; ok && provider.LlamaCpp != nil {
		lc := provider.LlamaCpp
		if lc.ModelsDir != "" {
			modelsDir = lc.ModelsDir
		}
		token = hfToken(lc.HFToken)
		ctxSize = effectiveLlamaContext(lc)
		parallel = effectiveLlamaParallel(lc)
		configuredRef, parseErr := llamacpp.ParseRef(lc.Repo)
		if parseErr == nil && configuredRef.Repo == ref.Repo {
			explicitQuant := ref.Quant != ""
			if lc.File != "" && !explicitQuant {
				ref.File = lc.File
			}
			if !explicitQuant {
				switch {
				case lc.Quant != "":
					ref.Quant = lc.Quant
				case configuredRef.Quant != "":
					ref.Quant = configuredRef.Quant
				}
			}
			if lc.Revision != "" {
				ref.Revision = lc.Revision
			}
			if lc.MMProjFile != "" {
				ref.MMProjFile = lc.MMProjFile
			}
		}
	}
	dl := llamacpp.NewDownloader(modelsDir, token, logger)
	dl.ContextTokens = ctxSize
	dl.Parallel = parallel
	return ref, dl, nil
}

func effectiveLlamaContext(cfg *config.LlamaCppConfig) int {
	if cfg != nil && cfg.CtxSize > 0 {
		return cfg.CtxSize
	}
	return 4096
}

func effectiveLlamaParallel(cfg *config.LlamaCppConfig) int {
	if cfg != nil && cfg.Parallel > 0 {
		return cfg.Parallel
	}
	return 1
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

/* defaultModelsDir is where autonomously downloaded GGUF weights are stored. */
func defaultModelsDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".agent-smith", "models")
	}
	return filepath.Join("data", "models")
}

/*
hfToken resolves the Hugging Face access token: an explicit config value wins,
otherwise the conventional HF_TOKEN environment variable (the same source
llama.cpp's own -hf uses).
*/
func hfToken(configured string) string {
	if configured != "" {
		return configured
	}
	return os.Getenv("HF_TOKEN")
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
runRefine drives the opt-in refinement loop: it grounds the agent (RAG/web/
Context7, same posture as the server), builds the strict OpenAI judge from the
configured openai provider, and regenerates the answer with the judge's critique
until the judge passes it as USABLE or the iteration budget is exhausted. The
agent's advisory verifier is disabled on this path because the judge IS the
validation, and feeding the appended advisory back into the next round would
pollute both the answer and the grounding. The loop never fabricates a pass; on
exhaustion it prints the least-fabricated attempt and an honest non-usable note.
*/
func runRefine(ctx context.Context, cfg *config.Config, f flags, a *agent.Agent, logger *slog.Logger) error {
	oa := cfg.Providers["openai"]
	judge := refine.NewOpenAIJudge(oa.APIKey, oa.BaseURL, oa.Model)
	if judge == nil {
		return errors.New("--refine-loop requires the OpenAI judge: set OPENAI_API_KEY and a real OPENAI_MODEL (e.g. gpt-5.5)")
	}

	/* Ground each round like the server, and take the raw answer (no appended advisory). */
	a.WebSearch = !f.disableWeb
	a.Verifier = nil

	gen := func(gctx context.Context, task, brief string) (string, error) {
		prompt := task
		if brief != "" {
			prompt = task + "\n\n[Refinement brief — improve grounding, scoping, and labelling ONLY; do NOT fabricate]\n" + brief
		}
		return a.Run(gctx, agent.NewSession(), prompt)
	}

	logger.Info("refine loop: enabled", "judge", judge.Name(), "max_iters", f.refineIters, "round_timeout", f.refineTO.String())
	res, err := refine.Run(ctx, f.prompt, gen, judge, refine.LoopConfig{MaxIters: f.refineIters, RoundTimeout: f.refineTO})
	if err != nil {
		return err
	}

	printRefineResult(os.Stdout, res)
	return nil
}

/* printRefineResult writes the final answer followed by the per-round audit ledger. */
func printRefineResult(w io.Writer, res refine.Result) {
	fmt.Fprintln(w, res.FinalAnswer)
	fmt.Fprintf(w, "\n--- refinement ledger (%d round(s), outcome: %s) ---\n", len(res.Rounds), res.Reason)
	if !res.Usable {
		fmt.Fprintln(w, "NOTE: the loop did NOT reach a usable answer; the least-fabricated attempt is shown above. Not a confirmed result.")
	}
	for _, r := range res.Rounds {
		status := "NOT_USABLE"
		if r.Verdict.Usable {
			status = "USABLE"
		}
		fmt.Fprintf(w, "round %d [%s, %dms]: %s\n", r.Iter, status, r.DurationMs, r.Verdict.Reasons)
		if len(r.Verdict.FailureModes) > 0 {
			fmt.Fprintf(w, "  failure modes: %s\n", strings.Join(r.Verdict.FailureModes, ", "))
		}
	}
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
