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

	/*
		Auto-tune GPU layers whenever the operator has not pinned them: detect the
		host (GPU/VRAM/RAM) and pick an offload profile that makes the most of it.
		An explicit ctx_size is honored rather than overwritten — the tuner still
		chooses the layer count for the detected VRAM, but the operator's larger
		context wins so a system-prompt + RAG workload is not capped at the tuner's
		conservative partial-offload default (the extra KV cache then spills to host
		memory). Pinning gpu_layers disables auto-tuning entirely.
	*/
	if lc.GPULayers == 0 {
		if rec, ok := autoTuneLlama(ctx, lc, rc); ok {
			rc.GPULayers = rec.GPULayers
			if lc.CtxSize == 0 {
				rc.CtxSize = rec.CtxSize
				rc.KVCacheType = rec.KVCacheType
				if rc.Downloader != nil {
					rc.Downloader.ContextTokens = rc.CtxSize
				}
			}
			logger.Info("llamacpp: auto-tuned for detected hardware",
				"gpu_layers", rec.GPULayers, "ctx_size", rc.CtxSize, "backend", rec.Backend,
				"kv_cache_type", rc.KVCacheType, "operator_ctx", lc.CtxSize != 0,
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
		return llamacpp.RecommendRuntime(plan.Host, plan.Manifest.ModelBytes(), plan.Manifest.MMProjBytes(), lc.CtxSize), true
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
		return llamacpp.RecommendRuntime(host, modelBytes, statSize(lc.MMProjPath), lc.CtxSize), true
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
