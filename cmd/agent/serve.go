package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/server"
)

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
		if c, ok := prov.(llm.Closer); ok {
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
		Agentic:          f.agentic || serverCfg.Agent.Agentic,
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
