package cluster

import (
	"context"
	"testing"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

func TestScheduler(t *testing.T) {
	t.Run("it_pins_a_single_backend_when_mode_is_not_auto", func(t *testing.T) {
		cfg := testConfig()
		cfg.Cluster.Mode = BackendLlamaRPC
		s := newScheduler(cfg, NewManager(cfg, nil, discardLogger(), nil), discardLogger())
		order := s.candidateOrder(cfg.Models[0])
		if len(order) != 1 || order[0] != BackendLlamaRPC {
			t.Fatalf("pinned order = %v, want [llama_cpp_rpc]", order)
		}
	})

	t.Run("it_uses_preferred_backends_in_auto_mode", func(t *testing.T) {
		cfg := testConfig()
		s := newScheduler(cfg, NewManager(cfg, nil, discardLogger(), nil), discardLogger())
		order := s.candidateOrder(cfg.Models[0])
		if len(order) == 0 || order[0] != BackendExo {
			t.Fatalf("auto order = %v, want exo first", order)
		}
	})

	t.Run("it_excludes_local_from_cluster_memory_but_fits_coordinator", func(t *testing.T) {
		cfg := testConfig()
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		/* 70B-Q4 needs 48 GB: fits the 64 GB coordinator for local. */
		if !s.memoryFits(BackendLocal, cfg.Models[0]) {
			t.Error("local should fit a 48 GB model on a 64 GB coordinator")
		}
		/* Cluster total (64+24=88) easily fits. */
		if !s.memoryFits(BackendExo, cfg.Models[0]) {
			t.Error("exo should fit using whole-cluster memory")
		}
		/* A model needing 80 GB does NOT fit the coordinator alone (local). */
		big := cfg.Models[0]
		big.MinMemoryGB = 80
		if s.memoryFits(BackendLocal, big) {
			t.Error("local must not claim to fit an 80 GB model on a 64 GB coordinator")
		}
		if !s.memoryFits(BackendExo, big) {
			t.Error("cluster (88 GB) should fit an 80 GB model")
		}
	})

	t.Run("it_falls_back_to_local_when_no_cluster_backend_is_installed", func(t *testing.T) {
		cfg := testConfig()
		/* exo/mlx/llama binaries are absent in the test env -> Probe Installed=false. */
		local := &fakeProvider{name: "ollama", tokens: []string{"ok"}}
		mgr := NewManager(cfg, local, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		b, err := s.SelectBackend(context.Background(), llm.ChatRequest{}, cfg.Models[0])
		if err != nil {
			t.Fatalf("SelectBackend: %v", err)
		}
		if b.Name() != BackendLocal {
			t.Fatalf("selected %q, want local fallback", b.Name())
		}
	})

	t.Run("it_errors_in_strict_mode_with_no_cluster_backend", func(t *testing.T) {
		cfg := testConfig()
		cfg.Runtime.StrictCluster = true
		local := &fakeProvider{name: "ollama", tokens: []string{"ok"}}
		mgr := NewManager(cfg, local, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		_, err := s.SelectBackend(context.Background(), llm.ChatRequest{}, cfg.Models[0])
		if err == nil {
			t.Fatal("expected strict_cluster error when no cluster backend is available")
		}
	})
}
