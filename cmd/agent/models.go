package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm/llamacpp"
)

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
