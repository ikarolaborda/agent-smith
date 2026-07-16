/*
MLX/JACCL backend via a Python sidecar. Go never imports MLX: it generates the
distributed hostfile from the node config, sets the MLX environment (optionally
MLX_METAL_FAST_SYNCH=1), launches the Python sidecar (scripts/mlx_sidecar.py,
which wraps mlx_lm.server / mlx.launch and exposes an OpenAI-compatible HTTP
endpoint), then proxies chat through it. If the sidecar reports that distributed
RDMA/ring setup is missing, Probe surfaces a clear diagnostic instead of failing
silently.
*/
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

/* mlxBackend launches and proxies the MLX python sidecar. */
type mlxBackend struct {
	httpBackend
	cfg          RuntimeConfig
	hostfilePath string
}

/* newMLXBackend constructs the MLX backend (not yet started). */
func newMLXBackend(cfg RuntimeConfig, logger *slog.Logger, m *Collector) *mlxBackend {
	return &mlxBackend{
		httpBackend: httpBackend{
			name:    BackendMLXJACCL,
			baseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.MLX.Port),
			logger:  logger,
			metrics: m,
		},
		cfg: cfg,
	}
}

/*
Probe reports whether the Python interpreter and the sidecar script are present.
It does not import MLX itself (that is the sidecar's job); a missing sidecar
script or interpreter is the actionable signal here.
*/
func (b *mlxBackend) Probe(ctx context.Context) (*BackendCapabilities, error) {
	caps := &BackendCapabilities{Endpoint: b.baseURL, MaxContext: b.maxContext}
	if _, err := exec.LookPath(b.cfg.MLX.Python); err != nil {
		caps.Diagnostic = fmt.Sprintf("python interpreter %q not found", b.cfg.MLX.Python)
		return caps, nil
	}
	if _, err := os.Stat(b.cfg.MLX.Sidecar); err != nil {
		caps.Diagnostic = fmt.Sprintf("MLX sidecar script not found at %q", b.cfg.MLX.Sidecar)
		return caps, nil
	}
	caps.Installed = true
	if !b.cfg.MLX.AllowUnboundedKV {
		caps.Diagnostic = "MLX backend disabled: mlx_lm.server has no enforceable KV-cache bound; set runtime.mlx.unsafe_allow_unbounded_kv only after independently containing the Metal OOM risk"
		return caps, nil
	}
	reachable, detail := b.probeEndpoint(ctx)
	caps.Available = reachable
	if !reachable {
		caps.Diagnostic = "MLX sidecar present but endpoint not reachable yet: " + detail
	}
	return caps, nil
}

/*
Start writes the distributed hostfile, assembles the sidecar environment, and
launches the sidecar. For a single node the hostfile has one entry and the
sidecar runs mlx_lm.server directly; for multiple nodes the sidecar uses
mlx.launch with the hostfile.
*/
func (b *mlxBackend) Start(ctx context.Context, cfg BackendConfig) error {
	b.servedName = cfg.Model.ServedName
	b.maxContext = cfg.Model.ContextTokens
	if !b.cfg.MLX.AllowUnboundedKV {
		return fmt.Errorf("mlx_jaccl: refusing launch because mlx_lm.server cannot enforce the configured context/KV memory bound; use a bounded backend or explicitly set unsafe_allow_unbounded_kv")
	}

	if cfg.Model.Path == "" {
		return fmt.Errorf("mlx_jaccl: model %q has no path (point models[].path at the MLX model dir or HF id)", cfg.Model.ID)
	}
	if _, err := exec.LookPath(b.cfg.MLX.Python); err != nil {
		return fmt.Errorf("mlx_jaccl: python %q not found: %w", b.cfg.MLX.Python, err)
	}
	if _, err := os.Stat(b.cfg.MLX.Sidecar); err != nil {
		return fmt.Errorf("mlx_jaccl: sidecar %q not found: %w", b.cfg.MLX.Sidecar, err)
	}

	hostfile, err := b.writeHostfile(append([]Node{cfg.Coordinator}, cfg.Workers...))
	if err != nil {
		return err
	}
	b.hostfilePath = hostfile

	args := []string{
		b.cfg.MLX.Sidecar,
		"--model", cfg.Model.Path,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(b.cfg.MLX.Port),
		"--hostfile", hostfile,
	}
	/*
		Do not map ContextTokens to mlx_lm.server --max-tokens: that flag limits
		generated OUTPUT, not the prompt/KV context. Pretending otherwise creates
		a false memory-safety guarantee.
	*/
	if b.cfg.MLX.Pipeline {
		args = append(args, "--pipeline")
	}
	args = append(args, b.cfg.MLX.ExtraArgs...)

	env := []string{}
	if b.cfg.MLX.FastSync {
		env = append(env, "MLX_METAL_FAST_SYNCH=1")
	}

	b.logger.Info("cluster: launching MLX sidecar", "hostfile", hostfile, "pipeline", b.cfg.MLX.Pipeline, "fast_sync", b.cfg.MLX.FastSync)
	b.sup = newSupervisor(spawnSpec{
		name:      BackendMLXJACCL,
		path:      b.cfg.MLX.Python,
		args:      args,
		env:       env,
		readyAddr: fmt.Sprintf("127.0.0.1:%d", b.cfg.MLX.Port),
	}, cfg.Runtime.ProcessRestart, cfg.Runtime.MaxRestartAttempts, b.logger, b.metrics)
	return b.sup.Start(ctx)
}

/*
hostEntry is one line of the MLX hostfile in the JSON shape mlx.launch expects:
an ssh-reachable host plus the GPU slot count. We map gpu_cores onto slots so
the sidecar can size the distributed group.
*/
type hostEntry struct {
	SSH   string `json:"ssh"`
	Slots int    `json:"slots"`
}

/* writeHostfile renders the node topology into an MLX-compatible hostfile. */
func (b *mlxBackend) writeHostfile(nodes []Node) (string, error) {
	entries := make([]hostEntry, 0, len(nodes))
	for _, n := range nodes {
		slots := n.GPUCores
		if slots <= 0 {
			slots = 1
		}
		entries = append(entries, hostEntry{SSH: n.Host, Slots: slots})
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return "", fmt.Errorf("mlx_jaccl: marshal hostfile: %w", err)
	}
	dir, err := os.MkdirTemp("", "mlx-hostfile-")
	if err != nil {
		return "", fmt.Errorf("mlx_jaccl: temp dir: %w", err)
	}
	path := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("mlx_jaccl: write hostfile: %w", err)
	}
	return path, nil
}
