package main

import (
	"log/slog"
	"os"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/context7"
	"github.com/ikarolaborda/agent-smith/internal/rag"
	"github.com/ikarolaborda/agent-smith/internal/tools"
	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
	"github.com/ikarolaborda/agent-smith/internal/web"
)

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
