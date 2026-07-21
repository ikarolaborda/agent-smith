package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/cluster"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/llm/abliteration"
	"github.com/ikarolaborda/agent-smith/internal/llm/anthropic"
	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
	"github.com/ikarolaborda/agent-smith/internal/llm/ollama"
	"github.com/ikarolaborda/agent-smith/internal/llm/openai"
)

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
		return buildLlamaCppProvider(ctx, pcfg, f.autoPickModel, logger)
	default:
		return nil, fmt.Errorf("unknown provider %q (registered: %v)", name, llm.Names())
	}
}

/*
buildLlamaCppProvider downloads the model if needed, starts a local llama-server,
and returns a Provider that owns the subprocess. The returned provider exposes
Close; callers must defer it so the server is stopped on exit.
*/
func buildLlamaCppProvider(ctx context.Context, pcfg config.ProviderConfig, autoPick bool, logger *slog.Logger) (llm.Provider, error) {
	lc := pcfg.LlamaCpp
	if lc == nil {
		return nil, errors.New("llamacpp: provider selected but no llama_cpp config block")
	}

	/*
		Free memory a prior agent-smith may have leaked before admission measures it:
		a llama-server we launched whose supervisor died keeps running and holds VRAM,
		blocking this load. Reaping is conservative — only our own, only orphaned.
	*/
	llamacpp.ReapOrphanedServers(logger)

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

	applyLlamaAutoTune(ctx, lc, &rc, logger)

	rt, picked, err := startLlamaRuntime(ctx, lc, rc, autoPick || lc.AutoPickModel, logger)
	if err != nil {
		return nil, err
	}
	/*
		Advertise the model actually being served: after a substitution, keeping
		the configured display name would tell callers (and the web model picker)
		they are talking to the big model while the auto-picked one answers.
	*/
	if picked != "" {
		pcfg.Model = displayModelName(picked)
	}
	prov, err := llamacpp.New(llamacpp.Config{Runtime: rt, Model: pcfg.Model, APIKey: pcfg.APIKey})
	if err != nil {
		_ = rt.Close(context.Background())
		return nil, err
	}
	return prov, nil
}

/*
applyLlamaAutoTune tunes GPU layers whenever the operator has not pinned them:
detect the host (GPU/VRAM/RAM) and pick an offload profile that makes the most
of it. An explicit ctx_size is honored rather than overwritten — the tuner
still chooses the layer count for the detected VRAM, but the operator's larger
context wins so a system-prompt + RAG workload is not capped at the tuner's
conservative partial-offload default (the extra KV cache then spills to host
memory). Pinning gpu_layers disables auto-tuning entirely. Re-run after an
auto-pick substitution so ctx/KV are recomputed for the replacement model's
sizes instead of inherited from the rejected one.
*/
func applyLlamaAutoTune(ctx context.Context, lc *config.LlamaCppConfig, rc *llamacpp.RuntimeConfig, logger *slog.Logger) {
	if lc.GPULayers != 0 {
		return
	}
	rec, ok := autoTuneLlama(ctx, lc, *rc)
	if !ok {
		return
	}
	rc.GPULayers = rec.GPULayers
	if lc.CtxSize == 0 {
		rc.CtxSize = rec.CtxSize
		rc.KVCacheType = rec.KVCacheType
		if rc.Downloader != nil {
			rc.Downloader.ContextTokens = rc.CtxSize
		}
	}
	if lc.CtxSize > 0 && rec.NativeCtx > 0 && lc.CtxSize > rec.NativeCtx {
		logger.Warn("llamacpp: pinned ctx_size exceeds the model's native context; explicit config wins, but attention beyond the trained window needs rope scaling or degrades quality",
			"ctx_size", lc.CtxSize, "native_ctx", rec.NativeCtx)
	}
	logger.Info("llamacpp: auto-tuned for detected hardware",
		"gpu_layers", rec.GPULayers, "ctx_size", rc.CtxSize, "backend", rec.Backend,
		"kv_cache_type", rc.KVCacheType, "operator_ctx", lc.CtxSize != 0,
		"rationale", strings.Join(rec.Rationale, "; "))
}

/*
startLlamaRuntime starts the configured runtime and, when auto-pick is enabled,
substitutes progressively smaller abliterated catalog models on fit refusal
until one launches. Substitution is deliberately narrow: only repo-sourced
models qualify (an operator-pinned model_path fails strictly), only a genuine
*llamacpp.FitError triggers it (download, auth, or generic runtime failures
surface unchanged), and the visited set bounds the loop — every retry must pick
a new ref, and AutoPick only proposes strictly smaller candidates than the ref
that just failed, so the walk is monotone and finite.
*/
func startLlamaRuntime(ctx context.Context, lc *config.LlamaCppConfig, rc llamacpp.RuntimeConfig, autoPick bool, logger *slog.Logger) (*llamacpp.Runtime, string, error) {
	rt := llamacpp.NewRuntime(rc)
	err := rt.Start(ctx)
	if err == nil || !autoPick || rc.Ref == nil {
		return rt, "", err
	}

	visited := map[string]bool{rc.Ref.Repo: true}
	for {
		var fitErr *llamacpp.FitError
		if !errors.As(err, &fitErr) {
			return nil, "", err
		}
		pick, ok := llamacpp.AutoPick(fitErr.Report, nil, llamacpp.DefaultFitPolicy())
		if !ok {
			return nil, "", fmt.Errorf("llamacpp: auto-pick found no abliterated catalog model that fits this host: %w", err)
		}
		ref, perr := llamacpp.ParseRef(pick.Model.Ref)
		if perr != nil || visited[ref.Repo] {
			return nil, "", err
		}
		visited[ref.Repo] = true
		logger.Warn("llamacpp: configured model does not fit this host; auto-picking a smaller abliterated model",
			"rejected", rc.Ref.Repo, "picked", pick.Model.Ref,
			"params_b", pick.Model.ParamsB, "code_optimized", pick.Model.CodeOptimized,
			"estimated_runtime_bytes", pick.EstimatedBytes, "reason", "fit_refusal",
			"operator_ctx_dropped", lc.CtxSize != 0,
			"disable_with", "--auto-pick-model=false / AUTO_MODEL=0")
		/*
			ParseRef yields a clean ref, dropping the File/Quant/MMProj pins that
			belong to the rejected artifact; the downloader re-resolves the picked
			repo's own artifacts and the fit gate re-validates against live memory.
			The operator's ctx pin is dropped too: it was sized for the rejected
			model (the example config pins 16384 precisely because that model is
			large), and inheriting it would make every smaller candidate fail on
			hosts where the KV reserve alone overflowed the budget. The tuner
			re-derives ctx/KV for the replacement; a pinned gpu_layers still
			disables tuning entirely, in which case the conservative default
			context below is what the substituted model launches with.
		*/
		rc.Ref = &ref
		rc.CtxSize = effectiveLlamaContext(nil)
		rc.KVCacheType = ""
		if rc.Downloader != nil {
			rc.Downloader.ContextTokens = rc.CtxSize
		}
		tuneCfg := *lc
		tuneCfg.CtxSize = 0
		applyLlamaAutoTune(ctx, &tuneCfg, &rc, logger)
		rt = llamacpp.NewRuntime(rc)
		if err = rt.Start(ctx); err == nil {
			logger.Info("llamacpp: auto-picked model launched", "model", pick.Model.Ref)
			return rt, pick.Model.Ref, nil
		}
	}
}

/*
displayModelName reduces a Hugging Face ref to the advertised model name:
the repo's own name without the uploader prefix or the -GGUF packaging suffix,
lowercased to match the naming style of the configured model labels.
*/
func displayModelName(ref string) string {
	name := ref
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, "-GGUF")
	return strings.ToLower(name)
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
		native := llamacpp.NativeContextFromPlan(plan)
		return llamacpp.RecommendRuntime(plan.Host, plan.Manifest.ModelBytes(), plan.Manifest.MMProjBytes(), lc.CtxSize, native), true
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
		native, _ := llamacpp.ReadGGUFContextLength(lc.ModelPath)
		return llamacpp.RecommendRuntime(host, modelBytes, statSize(lc.MMProjPath), lc.CtxSize, native), true
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
