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
		/* Neutralize the per-node safety budget here: this case tests aggregate
		   cluster-memory placement; the budget guard is tested separately. */
		cfg.Cluster.Nodes[1].SafeModelGB = 1000
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		/* The coordinator keeps 20 GB reserved: its safe local budget is 44 GB. */
		if s.memoryFits(BackendLocal, cfg.Models[0]) {
			t.Error("local must not fit a 48 GB model in a 44 GB safe budget")
		}
		/* Cluster total (64+24=88) easily fits. */
		if !s.memoryFits(BackendExo, cfg.Models[0]) {
			t.Error("exo should fit using whole-cluster memory")
		}
		/* A 50 GB model does NOT fit the coordinator alone (local budget 44) but
		   DOES fit distributed: coordinator slice ~0.73*50+reserve stays under 44
		   and the worker budget is neutralized here. */
		big := cfg.Models[0]
		big.MinMemoryGB = 50
		if s.memoryFits(BackendLocal, big) {
			t.Error("local must not claim to fit a 50 GB model on a 64 GB coordinator")
		}
		if !s.memoryFits(BackendExo, big) {
			t.Error("cluster should fit a 50 GB model whose per-node slices are safe")
		}
		/* An 80 GB model does NOT fit even distributed: the coordinator's own
		   RAM-proportional slice (~58 GB) overflows its 44 GB safe budget. The
		   pre-fix gate accepted this because it never bounded the coordinator. */
		huge := cfg.Models[0]
		huge.MinMemoryGB = 80
		if s.memoryFits(BackendExo, huge) {
			t.Error("cluster must refuse an 80 GB model whose coordinator slice overflows its safe budget")
		}
	})

	t.Run("it_prefers_single_node_when_the_model_fits_the_coordinator_alone", func(t *testing.T) {
		cfg := testConfig()
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		/* Coordinator 64 GB, reserve 20 -> safe budget 44 GB. */
		fits := cfg.Models[0]
		fits.MinMemoryGB = 34
		/* A 34 GB model fits the coordinator alone, so distributed backends are
		   skipped (single-node-first) — never tensor-split onto the fragile worker. */
		if s.memoryFits(BackendLlamaRPC, fits) {
			t.Error("llama_cpp_rpc must be skipped for a model that fits the coordinator alone")
		}
		if s.memoryFits(BackendExo, fits) {
			t.Error("exo must be skipped for a model that fits the coordinator alone")
		}
		/* Local single-node still fits it. */
		if !s.memoryFits(BackendLocal, fits) {
			t.Error("local should host a 34 GB model on a 64 GB coordinator")
		}
		/* force_distribute waives single-node-first; with the worker's safety
		   budget neutralized, distributed becomes eligible again. */
		cfg.Cluster.Nodes[1].SafeModelGB = 1000
		forced := fits
		forced.ForceDistribute = true
		if !s.memoryFits(BackendLlamaRPC, forced) {
			t.Error("force_distribute must re-enable distributed placement")
		}
	})

	t.Run("it_refuses_distributed_when_worker_slice_exceeds_its_safe_budget", func(t *testing.T) {
		cfg := testConfig()
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		/* 36 GB at 32k context: the coordinator slice fits its 44 GB budget, but
		   the worker's share + 16 GB compute reserve overflows its 12 GB budget,
		   so distributed is refused even with force_distribute. This is the guard
		   that prevents the worker freeze. */
		big := cfg.Models[0]
		big.MinMemoryGB = 36
		big.ContextTokens = 32768
		big.ForceDistribute = true
		if s.memoryFits(BackendLlamaRPC, big) {
			t.Error("must refuse distributed when the worker slice exceeds its safe budget")
		}
		/* Raising the worker's safe budget high enough re-admits it (the
		   coordinator was already within budget). */
		cfg.Cluster.Nodes[1].SafeModelGB = 1000
		if !s.memoryFits(BackendLlamaRPC, big) {
			t.Error("a large enough worker safe budget should re-admit distributed")
		}
	})

	t.Run("it_refuses_distributed_when_the_coordinator_slice_exceeds_its_budget", func(t *testing.T) {
		cfg := testConfig()
		/* Neutralize the worker budget so the coordinator is the only constraint.
		   An explicit split forces most of the model onto the coordinator. */
		cfg.Cluster.Nodes[1].SafeModelGB = 1000
		cfg.Runtime.Llama.TensorSplit = "0.9,0.1"
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		big := cfg.Models[0]
		big.MinMemoryGB = 60
		big.ContextTokens = 8192
		big.ForceDistribute = true
		if s.memoryFits(BackendExo, big) {
			t.Error("must refuse distributed when the COORDINATOR slice (0.9*60) exceeds its 44 GB safe budget")
		}
	})

	t.Run("it_normalizes_proportional_tensor_split_weights", func(t *testing.T) {
		cfg := testConfig()
		/* "0.2,0.2" is a 50/50 split after llama.cpp normalization, NOT two 20%
		   slices. The pre-fix code read the last raw value (0.2) as an absolute
		   worker fraction and under-counted the worker's real slice. */
		cfg.Cluster.Nodes[1].SafeModelGB = 30
		cfg.Runtime.Llama.TensorSplit = "0.2,0.2"
		mgr := NewManager(cfg, nil, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		shares := s.nodeShares()
		if len(shares) != 2 || shares[0] < 0.49 || shares[0] > 0.51 || shares[1] < 0.49 || shares[1] > 0.51 {
			t.Fatalf("expected normalized 50/50 shares, got %v", shares)
		}
		/* 48 GB at 50/50 -> worker slice 24 + reserve 4 = 28 <= its 30 GB budget:
		   admitted. Reading 0.2 as absolute would have projected 48*0.2+4 = ~14,
		   masking the real 28 GB peak. */
		big := cfg.Models[0]
		big.MinMemoryGB = 48
		big.ContextTokens = 8192
		big.ForceDistribute = true
		if !s.memoryFits(BackendExo, big) {
			t.Error("a normalized 50/50 slice within budget should admit")
		}
		/* Dropping the worker budget below the real 28 GB slice must now refuse —
		   the under-count bug would have wrongly admitted. */
		cfg.Cluster.Nodes[1].SafeModelGB = 20
		if s.memoryFits(BackendExo, big) {
			t.Error("normalized share must expose the real 28 GB worker slice and refuse")
		}
	})

	t.Run("it_falls_back_to_local_when_no_cluster_backend_is_installed", func(t *testing.T) {
		cfg := testConfig()
		cfg.Models[0].MinMemoryGB = 34
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

	t.Run("it_refuses_an_unsafe_local_fallback", func(t *testing.T) {
		cfg := testConfig() // 48 GB model, 44 GB coordinator safe budget.
		local := &fakeProvider{name: "ollama", tokens: []string{"must-not-run"}}
		mgr := NewManager(cfg, local, discardLogger(), nil)
		s := newScheduler(cfg, mgr, discardLogger())
		if _, err := s.SelectBackend(context.Background(), llm.ChatRequest{}, cfg.Models[0]); err == nil {
			t.Fatal("expected unsafe local fallback to be rejected")
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
