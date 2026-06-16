package cluster

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `
cluster:
  mode: auto
  coordinator: m5max
  transport_preference: [thunderbolt_rdma, thunderbolt_tcp, ethernet, local]
  nodes:
    - id: m5max
      host: m5max.local
      role: coordinator
      memory_gb: 64
      gpu_cores: 40
      priority: 100
      backends: [exo, mlx_jaccl, llama_cpp_rpc, local]
    - id: m5pro
      host: m5pro.local
      role: worker
      memory_gb: 24
      gpu_cores: 20
      priority: 50
      backends: [exo, mlx_jaccl, llama_cpp_rpc]
models:
  - id: 70b-q4-default
    path: /Users/shared/models/70b-q4
    context_tokens: 8192
    preferred_backends: [exo, mlx_jaccl, llama_cpp_rpc, local]
    min_memory_gb_estimate: 48
runtime:
  bind_host: 127.0.0.1
  api_port: 8080
  private_cluster_only: true
  strict_cluster: false
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func it_parses_and_defaults_the_sample_config(t *testing.T) {
	cfg, err := LoadClusterConfig(writeTemp(t, sampleConfig))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Cluster.Mode != "auto" {
		t.Errorf("mode = %q, want auto", cfg.Cluster.Mode)
	}
	if got := cfg.CoordinatorNode().ID; got != "m5max" {
		t.Errorf("coordinator = %q, want m5max", got)
	}
	workers := cfg.WorkerNodes()
	if len(workers) != 1 || workers[0].ID != "m5pro" {
		t.Errorf("workers = %+v, want [m5pro]", workers)
	}
	/* Normalize must have filled the runtime defaults. */
	if cfg.Runtime.MaxRestartAttempts != 3 {
		t.Errorf("max_restart_attempts default = %d, want 3", cfg.Runtime.MaxRestartAttempts)
	}
	if cfg.Runtime.Exo.Port != 52415 || cfg.Runtime.MLX.Port != 8081 || cfg.Runtime.Llama.Port != 8082 {
		t.Errorf("backend port defaults wrong: exo=%d mlx=%d llama=%d", cfg.Runtime.Exo.Port, cfg.Runtime.MLX.Port, cfg.Runtime.Llama.Port)
	}
	/* served_name defaults to id. */
	m, ok := cfg.ModelByID("70b-q4-default")
	if !ok || m.ServedName != "70b-q4-default" {
		t.Errorf("served_name default = %q, ok=%v", m.ServedName, ok)
	}
}

func TestConfig(t *testing.T) {
	t.Run("it_parses_and_defaults_the_sample_config", it_parses_and_defaults_the_sample_config)

	t.Run("it_derives_allowlist_from_nodes_when_unset", func(t *testing.T) {
		cfg, err := LoadClusterConfig(writeTemp(t, sampleConfig))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		allowed := cfg.AllowedHosts()
		if !allowed["m5max.local"] || !allowed["m5pro.local"] {
			t.Errorf("derived allowlist = %v, want both node hosts", allowed)
		}
	})

	t.Run("it_rejects_non_loopback_bind_under_private_cluster_only", func(t *testing.T) {
		bad := sampleConfig + "\n"
		bad = replaceBindHost(bad, "0.0.0.0")
		_, err := LoadClusterConfig(writeTemp(t, bad))
		if err == nil {
			t.Fatal("expected validation error for non-loopback bind under private_cluster_only")
		}
	})

	t.Run("it_rejects_config_without_models", func(t *testing.T) {
		noModels := `
cluster:
  coordinator: a
  nodes:
    - id: a
      host: 127.0.0.1
      role: coordinator
`
		_, err := LoadClusterConfig(writeTemp(t, noModels))
		if err == nil {
			t.Fatal("expected error: at least one model is required")
		}
	})
}

/* replaceBindHost swaps the loopback bind_host in the sample for a test value. */
func replaceBindHost(body, host string) string {
	return strings.Replace(body, "bind_host: 127.0.0.1", "bind_host: "+host, 1)
}
