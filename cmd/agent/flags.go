package main

import (
	"flag"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/refine"
)

/* flags groups the CLI flag values so we can pass them around as one value. */
type flags struct {
	configPath                     string
	provider                       string
	model                          string
	prompt                         string
	stream                         bool
	serve                          bool
	addr                           string
	ingest                         bool
	collection                     string
	source                         string
	embedder                       string
	embedModel                     string
	ragDir                         string
	disableRAG                     bool
	disableWeb                     bool
	disableC7                      bool
	agentic                        bool
	graphExpand                    bool
	evalRAG                        string
	clusterCfg                     string
	workspace                      string
	ragMaxChunks                   int
	buildDataset                   bool
	datasetSrc                     string
	datasetOut                     string
	verifyCVE                      bool
	validateVuln                   bool
	allowExec                      bool
	execImageDigest                string
	researchMode                   bool
	researchDir                    string
	researchToken                  string
	researchActor                  string
	researchRoots                  string
	researchRuntime                string
	researchWorkers                int
	researchNoveltySources         string
	researchSourceBundles          string
	researchSourcePublicKey        string
	researchSourcePrivateKey       string
	signResearchSourceBundles      string
	researchSourceManifestLifetime time.Duration
	researchArtifactKeys           string
	refineLoop                     bool
	refineIters                    int
	refineTO                       time.Duration
	pull                           string
	inspectModel                   string
	installRuntime                 bool
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
	flag.BoolVar(&f.agentic, "agentic", false, "enable agentic-RAG: the model plans and runs its own retrieval via the rag_search/graph_expand tools instead of one-shot augmentation (requires a tool-capable reasoning provider such as OpenAI/Anthropic; a model that ignores tools will answer less grounded, since the classic augmentation is skipped in this mode)")
	flag.BoolVar(&f.graphExpand, "graph-expand", false, "register the knowledge-graph graph_expand tool for agentic-RAG. Off by default: a hand-labeled cross-source benchmark (eval/fixtures/README.md) measured no recall lift over plain lexical widening and 2-4% precision of graph-added chunks, so the graph hop mostly adds noise the model must filter. Enable to experiment on corpora with strong cross-document structure.")
	flag.StringVar(&f.evalRAG, "eval-rag", "", "offline retrieval evaluation: run the fixtures JSON at this path (list of {query, relevant_chunk_ids}) through plain search vs graph-widened search, print recall@k as JSON, and exit (no model needed)")
	flag.BoolVar(&f.disableWeb, "no-web-search", false, "operator kill switch for the web-grounding gate (overrides all per-request flags)")
	flag.BoolVar(&f.disableC7, "no-context7", false, "operator kill switch for Context7 documentation augmentation (otherwise on when CONTEXT7_API_KEY is set)")
	flag.StringVar(&f.clusterCfg, "cluster-config", "", "path to a cluster YAML config; enables clusterized inference (exo/MLX/llama.cpp RPC) with local fallback")
	flag.StringVar(&f.workspace, "workspace", "", "enable agentic project work: directory the agent may modify via file_write/file_edit (sandboxed). Unset = read-only.")
	flag.IntVar(&f.ragMaxChunks, "rag-max-chunks", 0, "override how many RAG chunks are injected per request (0 = default 4). Raise for large-context cluster models to improve grounding.")
	flag.BoolVar(&f.verifyCVE, "verify-cve", false, "verify CVE identifiers in answers against the NIST NVD primary source and append a non-destructive advisory note (network egress; reads NVD_API_KEY from env if set)")
	flag.BoolVar(&f.validateVuln, "validate-vuln", false, "cross-validate vulnerability-research answers against independent models (OpenAI via API; Anthropic via the Claude Code CLI / Max subscription) and append a non-authoritative advisory (network egress; drives the Max subscription programmatically)")
	flag.BoolVar(&f.allowExec, "allow-exec", false, "enable the OPT-IN container-contained execution tool (ADR 0003): the agent may run fixed apparatus operations (fuzz/reproduce/triage) inside an ephemeral, network-isolated, read-only Docker container mounting --workspace. OFF by default; requires --workspace and Docker. Each run is audited.")
	flag.StringVar(&f.execImageDigest, "exec-image-digest", "", "pin the contained-exec apparatus image to an exact local image ID (sha256:<hex>, from `docker images --no-trunc --quiet php74-asan`). With --pull=never this makes image resolution fail closed on any other content, defeating a local re-tag. Empty = resolve by tag (unpinned).")
	flag.BoolVar(&f.researchMode, "research-mode", false, "enable the authenticated durable research control plane (requires a bearer token and fixed workspace roots)")
	flag.StringVar(&f.researchDir, "research-dir", ".agent-smith/research", "private SQLite/artifact directory used by --research-mode")
	flag.StringVar(&f.researchToken, "research-token", "", "bootstrap bearer token for --research-mode (minimum 32 characters; prefer AGENT_SMITH_RESEARCH_TOKEN)")
	flag.StringVar(&f.researchActor, "research-actor", "local-operator", "audit principal ID for the bootstrap research credential")
	flag.StringVar(&f.researchRoots, "research-workspace-roots", "", "comma-separated existing directories permitted as research workspaces; defaults to --workspace when set")
	flag.StringVar(&f.researchRuntime, "research-container-runtime", "", "optional verified Docker runtime for research workers (for example runsc for gVisor)")
	flag.IntVar(&f.researchWorkers, "research-workers", 1, "global research worker concurrency (per-campaign concurrency remains one)")
	flag.StringVar(&f.researchNoveltySources, "research-novelty-sources", "", "path to a bounded JSON array of fixed HTTPS novelty lookup sources; empty disables lookup egress")
	flag.StringVar(&f.researchSourceBundles, "research-source-bundles", "", "path to an Ed25519-signed fixed HTTPS source-bundle manifest; empty disables network source acquisition")
	flag.StringVar(&f.researchSourcePublicKey, "research-source-bundle-public-key", "", "trusted PEM Ed25519 public key used to verify --research-source-bundles")
	flag.StringVar(&f.researchSourcePrivateKey, "research-source-bundle-private-key", "", "PEM Ed25519 private key used only with --sign-research-source-bundles")
	flag.StringVar(&f.signResearchSourceBundles, "sign-research-source-bundles", "", "sign an unsigned source array and write a canonical manifest envelope to stdout")
	flag.DurationVar(&f.researchSourceManifestLifetime, "research-source-manifest-lifetime", 30*24*time.Hour, "validity of a newly signed source-bundle manifest (maximum 90 days)")
	flag.StringVar(&f.researchArtifactKeys, "research-artifact-keys", "", "comma-separated 0600 files containing hex AES-256 artifact keys; first key is active and remaining keys enable rotation")
	flag.BoolVar(&f.refineLoop, "refine-loop", false, "OPT-IN single-shot refinement loop (requires --prompt + OpenAI judge): regenerate the answer with the gpt-5.x judge's critique until it is judged USABLE (grounded, feasible, honestly scoped) or the iteration budget is exhausted. Anti-fabrication: an honest negative passes; the loop never fakes a pass. CLI-only.")
	flag.IntVar(&f.refineIters, "refine-max-iters", refine.DefaultMaxIters, "maximum refinement iterations when --refine-loop is set")
	flag.DurationVar(&f.refineTO, "refine-timeout", refine.DefaultRoundTimeout, "per-round timeout (generate+judge) when --refine-loop is set")
	flag.StringVar(&f.pull, "pull", "", "download a GGUF model from Hugging Face and exit (e.g. hf.co/ggml-org/gemma-3-1b-it-GGUF:Q4_K_M). Uses the llamacpp provider's models_dir/hf_token when configured.")
	flag.StringVar(&f.inspectModel, "inspect-model", "", "resolve a GGUF artifact manifest, inspect this host, print the fit report as JSON, and exit without downloading model data")
	flag.BoolVar(&f.installRuntime, "install-runtime", false, "detect this host's OS/GPU, download the matching prebuilt llama.cpp llama-server (Vulkan by default), link it onto PATH, and exit. Bootstraps the llamacpp provider's runtime dependency.")
	flag.Parse()
	return f
}
