/*
llama.cpp RPC backend (experimental, private-network only). On worker nodes the
operator runs `rpc-server` (built with -DGGML_RPC=ON); on the coordinator this
backend launches `llama-server` with --rpc pointing at each worker's host:port,
which distributes weights and KV cache across local + remote ggml devices.
llama-server exposes an OpenAI-compatible /v1 endpoint, so chat reuses the
shared HTTP transport. We warn (never silently proceed) when an RPC host is on a
non-private interface, since RPC has no authentication.
*/
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

/* llamaBackend orchestrates llama-server + remote rpc-server hosts. */
type llamaBackend struct {
	httpBackend
	cfg RuntimeConfig
}

/* newLlamaBackend constructs the llama.cpp RPC backend (not yet started). */
func newLlamaBackend(cfg RuntimeConfig, logger *slog.Logger, m *Collector) *llamaBackend {
	return &llamaBackend{
		httpBackend: httpBackend{
			name:    BackendLlamaRPC,
			baseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", cfg.Llama.Port),
			logger:  logger,
			metrics: m,
		},
		cfg: cfg,
	}
}

/* Probe reports llama-server installation and current endpoint reachability. */
func (b *llamaBackend) Probe(ctx context.Context) (*BackendCapabilities, error) {
	caps := &BackendCapabilities{Endpoint: b.baseURL, MaxContext: b.maxContext}
	if _, err := exec.LookPath(b.cfg.Llama.Server); err != nil {
		caps.Diagnostic = fmt.Sprintf("llama-server %q not found (build llama.cpp with -DGGML_RPC=ON): %v", b.cfg.Llama.Server, err)
		return caps, nil
	}
	caps.Installed = true
	reachable, detail := b.probeEndpoint(ctx)
	caps.Available = reachable
	if !reachable {
		caps.Diagnostic = "llama-server installed but endpoint not reachable yet: " + detail
	}
	return caps, nil
}

/*
Start resolves the RPC host list (explicit config or derived from worker nodes),
warns about any non-private RPC target, and launches llama-server with the
model, RPC hosts, GPU-layer offload, and optional tensor split.
*/
func (b *llamaBackend) Start(ctx context.Context, cfg BackendConfig) error {
	b.servedName = cfg.Model.ServedName
	b.maxContext = cfg.Model.ContextTokens

	if cfg.Model.Path == "" {
		return fmt.Errorf("llama_cpp_rpc: model %q has no path (point models[].path at the .gguf file)", cfg.Model.ID)
	}
	if _, err := exec.LookPath(b.cfg.Llama.Server); err != nil {
		return fmt.Errorf("llama_cpp_rpc: %q not found: %w", b.cfg.Llama.Server, err)
	}

	rpcHosts := b.resolveRPCHosts(cfg.Workers)
	for _, h := range rpcHosts {
		host := h
		if i := strings.LastIndex(h, ":"); i >= 0 {
			host = h[:i]
		}
		if !isPrivateHost(host) {
			b.logger.Warn("cluster: llama RPC host is not on a private interface; RPC is unauthenticated and must stay on a trusted Thunderbolt/LAN link", "rpc_host", h)
		}
	}

	args := []string{
		"--model", cfg.Model.Path,
		"--host", "127.0.0.1",
		"--port", strconv.Itoa(b.cfg.Llama.Port),
		"-ngl", strconv.Itoa(b.cfg.Llama.GPULayers),
	}
	if cfg.Model.ContextTokens > 0 {
		args = append(args, "--ctx-size", strconv.Itoa(cfg.Model.ContextTokens))
	}
	if len(rpcHosts) > 0 {
		args = append(args, "--rpc", strings.Join(rpcHosts, ","))
	}
	if b.cfg.Llama.TensorSplit != "" {
		args = append(args, "--tensor-split", b.cfg.Llama.TensorSplit)
	}
	if b.cfg.Llama.CacheDir != "" {
		args = append(args, "--slot-save-path", b.cfg.Llama.CacheDir)
	}
	args = append(args, b.cfg.Llama.ExtraArgs...)

	b.logger.Info("cluster: launching llama-server", "rpc_hosts", rpcHosts, "tensor_split", b.cfg.Llama.TensorSplit, "gpu_layers", b.cfg.Llama.GPULayers)
	b.sup = newSupervisor(spawnSpec{
		name:      BackendLlamaRPC,
		path:      b.cfg.Llama.Server,
		args:      args,
		readyAddr: fmt.Sprintf("127.0.0.1:%d", b.cfg.Llama.Port),
	}, cfg.Runtime.ProcessRestart, cfg.Runtime.MaxRestartAttempts, b.logger, b.metrics)
	return b.sup.Start(ctx)
}

/* resolveRPCHosts uses the explicit list, else derives host:RPCPort per worker. */
func (b *llamaBackend) resolveRPCHosts(workers []Node) []string {
	if len(b.cfg.Llama.RPCHosts) > 0 {
		return b.cfg.Llama.RPCHosts
	}
	out := make([]string, 0, len(workers))
	for _, w := range workers {
		if w.Host == "" {
			continue
		}
		out = append(out, net.JoinHostPort(w.Host, strconv.Itoa(b.cfg.Llama.RPCPort)))
	}
	return out
}
