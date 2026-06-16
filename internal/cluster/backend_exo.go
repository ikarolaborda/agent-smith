/*
exo backend. exo (https://github.com/exo-explore/exo) auto-discovers peers on
the local network and exposes a ChatGPT/OpenAI-compatible HTTP API, so it is the
highest-priority cluster backend: the Go control plane either connects to an
already-running exo service or launches one locally, then proxies chat through
its /v1 endpoint. exo itself handles the Apple-cluster orchestration and model
sharding across nodes; we only manage its process and route requests.
*/
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
)

/* exoBackend orchestrates or connects to an exo service. */
type exoBackend struct {
	httpBackend
	cfg RuntimeConfig
}

/* newExoBackend constructs the exo backend (not yet started). */
func newExoBackend(cfg RuntimeConfig, logger *slog.Logger, m *Collector) *exoBackend {
	base := exoBaseURL(cfg.Exo)
	return &exoBackend{
		httpBackend: httpBackend{
			name:    BackendExo,
			baseURL: base,
			logger:  logger,
			metrics: m,
		},
		cfg: cfg,
	}
}

/* exoBaseURL resolves the OpenAI base for exo, honoring an explicit endpoint. */
func exoBaseURL(r ExoRuntime) string {
	if r.Endpoint != "" {
		return strings.TrimRight(r.Endpoint, "/") + "/v1"
	}
	return fmt.Sprintf("http://127.0.0.1:%d/v1", r.Port)
}

/*
Probe reports whether exo is installed and reachable. An explicitly configured
endpoint counts as "installed" even if the binary is absent on this host, since
the service may run elsewhere on the private cluster.
*/
func (b *exoBackend) Probe(ctx context.Context) (*BackendCapabilities, error) {
	installed := b.cfg.Exo.Endpoint != ""
	if !installed {
		if _, err := exec.LookPath(b.cfg.Exo.Binary); err == nil {
			installed = true
		}
	}
	caps := &BackendCapabilities{Installed: installed, Endpoint: b.baseURL, MaxContext: b.maxContext}
	if !installed {
		caps.Diagnostic = fmt.Sprintf("exo not found: install exo or set runtime.exo.endpoint (looked for %q)", b.cfg.Exo.Binary)
		return caps, nil
	}
	reachable, detail := b.probeEndpoint(ctx)
	caps.Available = reachable
	if !reachable {
		caps.Diagnostic = "exo installed but endpoint not reachable yet: " + detail
	}
	return caps, nil
}

/*
Start connects to an existing exo endpoint when configured, otherwise launches a
local exo process and waits for its endpoint to come up. Model identity is
passed through at request time (exo selects the model per request), so Start
does not pin a model name.
*/
func (b *exoBackend) Start(ctx context.Context, cfg BackendConfig) error {
	b.servedName = cfg.Model.ServedName
	b.maxContext = cfg.Model.ContextTokens

	if b.cfg.Exo.Endpoint != "" {
		if ok, detail := b.probeEndpoint(ctx); !ok {
			return fmt.Errorf("exo: configured endpoint %s not reachable: %s", b.baseURL, detail)
		}
		b.logger.Info("cluster: exo connected", "endpoint", b.baseURL)
		return nil
	}

	if _, err := exec.LookPath(b.cfg.Exo.Binary); err != nil {
		return fmt.Errorf("exo: binary %q not found and no endpoint configured: %w", b.cfg.Exo.Binary, err)
	}
	args := append([]string{}, b.cfg.Exo.ExtraArgs...)
	b.sup = newSupervisor(spawnSpec{
		name:      BackendExo,
		path:      b.cfg.Exo.Binary,
		args:      args,
		readyAddr: fmt.Sprintf("127.0.0.1:%d", b.cfg.Exo.Port),
	}, cfg.Runtime.ProcessRestart, cfg.Runtime.MaxRestartAttempts, b.logger, b.metrics)
	return b.sup.Start(ctx)
}
