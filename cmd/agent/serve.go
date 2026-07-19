package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ikarolaborda/agent-smith/internal/config"
	"github.com/ikarolaborda/agent-smith/internal/llm"
	"github.com/ikarolaborda/agent-smith/internal/research/domain"
	"github.com/ikarolaborda/agent-smith/internal/research/novelty"
	"github.com/ikarolaborda/agent-smith/internal/research/runner"
	"github.com/ikarolaborda/agent-smith/internal/research/sourcefetch"
	"github.com/ikarolaborda/agent-smith/internal/server"
)

/*
serverStateDir is where the server persists lightweight UI state (the open
workspace folder) so it survives a restart. Empty when no home directory is
resolvable, which disables persistence rather than failing.
*/
func serverStateDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".agent-smith")
	}
	return ""
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
		if c, ok := prov.(llm.Closer); ok {
			defer func() { _ = c.Close(context.Background()) }()
		}
		extra = map[string]llm.Provider{prov.Name(): prov}
	}

	var researchMode *server.ResearchModeOptions
	if f.researchMode {
		token := strings.TrimSpace(f.researchToken)
		if token == "" {
			token = strings.TrimSpace(os.Getenv("AGENT_SMITH_RESEARCH_TOKEN"))
		}
		roots := splitNonEmpty(f.researchRoots)
		if len(roots) == 0 && strings.TrimSpace(f.workspace) != "" {
			roots = []string{f.workspace}
		}
		if token == "" {
			return errors.New("serve: --research-mode requires --research-token or AGENT_SMITH_RESEARCH_TOKEN")
		}
		if len(roots) == 0 {
			return errors.New("serve: --research-mode requires --research-workspace-roots or --workspace")
		}
		researchMode = &server.ResearchModeOptions{
			Enabled: true, DataDir: f.researchDir, WorkspaceRoots: roots,
			GlobalConcurrency: f.researchWorkers, CampaignConcurrency: 1,
			Credentials: []server.ResearchCredential{{
				Token:     token,
				Principal: domain.Principal{ID: f.researchActor, Name: f.researchActor, Roles: []domain.Role{domain.RoleAdmin}},
			}},
		}
		if strings.TrimSpace(f.researchNoveltySources) != "" {
			researchMode.NoveltySources, err = loadNoveltySources(f.researchNoveltySources)
			if err != nil {
				return err
			}
		}
		if strings.TrimSpace(f.researchSourceBundles) != "" {
			if strings.TrimSpace(f.researchSourcePublicKey) == "" {
				return errors.New("serve: --research-source-bundles requires --research-source-bundle-public-key")
			}
			researchMode.SourceManifest, err = loadVerifiedSourceManifest(f.researchSourceBundles, f.researchSourcePublicKey, time.Now().UTC())
			if err != nil {
				return err
			}
		}
		if f.allowExec {
			researchMode.RunnerBackend = runner.NewDockerBackend(runner.DockerOptions{Runtime: f.researchRuntime, RequireRootless: true})
		}
	}

	srv, err := server.New(server.Options{
		Addr:             f.addr,
		Config:           serverCfg,
		Workspace:        f.workspace,
		StateDir:         serverStateDir(),
		AllowExec:        f.allowExec,
		ExecImageDigest:  f.execImageDigest,
		Logger:           logger,
		RAG:              ragSvc,
		DisableRAG:       f.disableRAG,
		Agentic:          f.agentic || serverCfg.Agent.Agentic,
		GraphExpand:      f.graphExpand,
		WebSearchEnabled: !f.disableWeb,
		VerifyCVE:        f.verifyCVE,
		ValidateVuln:     f.validateVuln,
		Providers:        injected,
		ExtraProviders:   extra,
		ResearchMode:     researchMode,
	})
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}
	defer srv.Close()
	return srv.ListenAndServe(ctx)
}

func loadNoveltySources(path string) ([]novelty.Source, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("serve: open research novelty sources: %w", err)
	}
	defer file.Close()
	const maxSourceConfigBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(file, maxSourceConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("serve: read research novelty sources: %w", err)
	}
	if len(body) > maxSourceConfigBytes {
		return nil, errors.New("serve: research novelty source data exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var sources []novelty.Source
	if err := decoder.Decode(&sources); err != nil {
		return nil, fmt.Errorf("serve: decode research novelty sources: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("serve: trailing research novelty source data")
	}
	if len(sources) == 0 || len(sources) > 64 {
		return nil, errors.New("serve: research novelty sources must contain 1..64 entries")
	}
	return sources, nil
}

func loadSourceBundleSources(path string) ([]sourcefetch.Source, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("serve: open research source bundles: %w", err)
	}
	defer file.Close()
	const maxSourceConfigBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(file, maxSourceConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("serve: read research source bundles: %w", err)
	}
	if len(body) > maxSourceConfigBytes {
		return nil, errors.New("serve: research source bundle data exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var sources []sourcefetch.Source
	if err := decoder.Decode(&sources); err != nil {
		return nil, fmt.Errorf("serve: decode research source bundles: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("serve: trailing research source bundle data")
	}
	if len(sources) == 0 || len(sources) > 64 {
		return nil, errors.New("serve: research source bundles must contain 1..64 entries")
	}
	return sources, nil
}

func loadVerifiedSourceManifest(manifestPath, publicKeyPath string, now time.Time) (*sourcefetch.VerifiedManifest, error) {
	body, err := readBoundedSourceFile(manifestPath, 1<<20, "signed source manifest")
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope sourcefetch.SignedManifest
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("serve: decode signed source manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("serve: trailing signed source manifest data")
	}
	keyData, err := readBoundedSourceFile(publicKeyPath, 16<<10, "source manifest public key")
	if err != nil {
		return nil, err
	}
	publicKey, err := sourcefetch.ParsePublicKeyPEM(keyData)
	if err != nil {
		return nil, fmt.Errorf("serve: parse source manifest public key: %w", err)
	}
	verified, err := sourcefetch.VerifyManifest(envelope, publicKey, now)
	if err != nil {
		return nil, fmt.Errorf("serve: verify source manifest: %w", err)
	}
	return verified, nil
}

func runSignSourceBundles(f flags, output io.Writer) error {
	if strings.TrimSpace(f.researchSourcePrivateKey) == "" {
		return errors.New("--sign-research-source-bundles requires --research-source-bundle-private-key")
	}
	if f.researchSourceManifestLifetime <= 0 || f.researchSourceManifestLifetime > sourcefetch.MaxManifestLifetime {
		return errors.New("source manifest lifetime must be positive and no more than 90 days")
	}
	sources, err := loadSourceBundleSources(f.signResearchSourceBundles)
	if err != nil {
		return err
	}
	keyInfo, err := os.Stat(strings.TrimSpace(f.researchSourcePrivateKey))
	if err != nil {
		return fmt.Errorf("open source manifest private key: %w", err)
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm()&0o077 != 0 {
		return errors.New("source manifest private key must not be accessible by group or others")
	}
	keyData, err := readBoundedSourceFile(f.researchSourcePrivateKey, 16<<10, "source manifest private key")
	if err != nil {
		return err
	}
	privateKey, err := sourcefetch.ParsePrivateKeyPEM(keyData)
	if err != nil {
		return fmt.Errorf("parse source manifest private key: %w", err)
	}
	now := time.Now().UTC()
	envelope, err := sourcefetch.SignManifest(sourcefetch.Manifest{SchemaVersion: 1, IssuedAt: now, ExpiresAt: now.Add(f.researchSourceManifestLifetime), Sources: sources}, privateKey)
	if err != nil {
		return fmt.Errorf("sign source manifest: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(envelope); err != nil {
		return fmt.Errorf("write signed source manifest: %w", err)
	}
	return nil
}

func readBoundedSourceFile(path string, maximum int64, label string) ([]byte, error) {
	file, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("serve: open %s: %w", label, err)
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("serve: read %s: %w", label, err)
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("serve: %s data exceeds limit", label)
	}
	return body, nil
}

func splitNonEmpty(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
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
