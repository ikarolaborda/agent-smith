/*
Command agent is the CLI entrypoint. It parses flags, loads configuration,
wires a llm.Provider through to an agent.Agent, and either answers a single
--prompt or reads lines from stdin in interactive mode.

Provider packages are imported with a blank identifier so their init()
functions register themselves with the llm registry.
*/
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ikarolaborda/agent-smith/internal/agent"
	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/logging"
	"github.com/ikarolaborda/agent-smith/internal/server"
	"github.com/ikarolaborda/agent-smith/internal/tools/builtin"
)

func main() {
	f := parseFlags()
	if err := run(f); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		os.Exit(1)
	}
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

	/*
		Runtime bootstrap runs before config load: a fresh host may not have a
		usable config yet, and installing llama-server is exactly what makes the
		default llamacpp provider runnable in the first place.
	*/
	if f.installRuntime {
		return runInstallRuntime(ctx, f)
	}
	if f.signResearchSourceBundles != "" {
		return runSignSourceBundles(f, os.Stdout)
	}
	if f.signResearchApparatusCatalog != "" {
		return runSignApparatusCatalog(f, os.Stdout)
	}

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

	if f.evalRAG != "" {
		return runEvalRAG(ctx, cfg, f, logger)
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
		if c, ok := provider.(llm.Closer); ok {
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
		/*
			Agentic-RAG needs the rag_search tool registered so the model can drive
			its own retrieval. Register it whenever RAG is live; enabling the loop
			itself is gated by --agentic (or config) so the classic path is default.
		*/
		if err := a.Tools.Register(builtin.NewRAGSearchTool(ragSvc)); err != nil {
			return fmt.Errorf("register rag_search tool: %w", err)
		}
		if f.graphExpand {
			if err := a.Tools.Register(builtin.NewGraphExpandTool(ragSvc)); err != nil {
				return fmt.Errorf("register graph_expand tool: %w", err)
			}
		}
		a.Agentic = f.agentic || cfg.Agent.Agentic
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
