/*
Backend scheduler. SelectBackend turns the cluster mode, the model's preferred
backend order, node memory, the coordinator's enabled backends, and live health
into a single chosen, started backend. The ordering logic is a pure function of
config + health and is exercised directly by the unit tests; the only impure
step is ensuring the chosen backend is started, which the manager makes
idempotent.
*/
package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* defaultBackendOrder is used when a model declares no preferred_backends. */
var defaultBackendOrder = []string{BackendExo, BackendMLXJACCL, BackendLlamaRPC, BackendLocal}

/* scheduler implements Scheduler against a Manager. */
type scheduler struct {
	cfg    *ClusterConfig
	mgr    *Manager
	logger *slog.Logger
}

/* newScheduler constructs the scheduler bound to a manager. */
func newScheduler(cfg *ClusterConfig, mgr *Manager, logger *slog.Logger) *scheduler {
	return &scheduler{cfg: cfg, mgr: mgr, logger: logger}
}

/*
SelectBackend walks the candidate order, skips backends that are not enabled on
the coordinator or cannot fit the model in available memory, and returns the
first one it can bring up healthy. When no cluster backend succeeds it falls
back to the local backend unless runtime.strict_cluster forbids it.
*/
func (s *scheduler) SelectBackend(ctx context.Context, req llm.ChatRequest, model ModelConfig) (InferenceBackend, error) {
	order := s.candidateOrder(model)
	coordBackends := s.coordinatorBackends()
	var lastErr error

	for _, name := range order {
		if name == BackendLocal {
			/* local is handled as the explicit fallback below, never mid-order. */
			continue
		}
		if !coordBackends[name] {
			s.logger.Debug("cluster: backend not enabled on coordinator", "backend", name)
			continue
		}
		if !s.memoryFits(name, model) {
			s.logger.Warn("cluster: skipping backend, insufficient memory for model",
				"backend", name, "model", model.ID, "min_memory_gb", model.MinMemoryGB)
			continue
		}
		b := s.mgr.backend(name)
		if b == nil {
			continue
		}
		if caps, err := b.Probe(ctx); err == nil && !caps.Installed {
			s.logger.Info("cluster: backend not installed, skipping", "backend", name, "diagnostic", caps.Diagnostic)
			continue
		}
		if err := s.mgr.ensureStarted(ctx, name, model); err != nil {
			s.logger.Warn("cluster: backend failed to start, trying next", "backend", name, "err", err)
			lastErr = err
			continue
		}
		if h, err := b.Health(ctx); err == nil && h.Healthy {
			s.logger.Info("cluster: backend selected", "backend", name, "model", model.ID)
			return b, nil
		} else {
			lastErr = fmt.Errorf("%s: not healthy after start", name)
			s.logger.Warn("cluster: backend unhealthy after start, trying next", "backend", name)
		}
	}

	if s.cfg.Runtime.StrictCluster {
		if lastErr == nil {
			lastErr = errors.New("no cluster backend available")
		}
		return nil, fmt.Errorf("cluster: strict_cluster set and no cluster backend came up: %w", lastErr)
	}

	local := s.mgr.backend(BackendLocal)
	if local == nil {
		return nil, errors.New("cluster: no cluster backend available and no local fallback configured")
	}
	if caps, err := local.Probe(ctx); err != nil || !caps.Available {
		return nil, fmt.Errorf("cluster: no backend available (local fallback unusable): %w", lastErr)
	}
	if err := s.mgr.ensureStarted(ctx, BackendLocal, model); err != nil {
		return nil, err
	}
	s.logger.Info("cluster: falling back to local single-node backend", "model", model.ID)
	return local, nil
}

/*
candidateOrder resolves the ordered backend names to try. A pinned mode yields a
single-element order; "auto" uses the model's preferred_backends or the default.
*/
func (s *scheduler) candidateOrder(model ModelConfig) []string {
	mode := s.cfg.Cluster.Mode
	if mode != "" && mode != "auto" {
		return []string{mode}
	}
	if len(model.PreferredBackends) > 0 {
		return model.PreferredBackends
	}
	return defaultBackendOrder
}

/* coordinatorBackends is the set of backends enabled on the coordinator node. */
func (s *scheduler) coordinatorBackends() map[string]bool {
	out := map[string]bool{}
	coord := s.cfg.CoordinatorNode()
	for _, b := range coord.Backends {
		out[b] = true
	}
	/* local is always permitted on the coordinator, even if unlisted. */
	out[BackendLocal] = true
	return out
}

/*
memoryFits reports whether a model's estimated minimum fits the memory the
backend can draw on: the whole reachable cluster for distributed backends, just
the coordinator for the local backend. A zero estimate means "unknown" and is
treated as fitting (the operator opted out of the check).

Placement uses only the statically configured memory_gb of reachable nodes; the
runtime memory-pressure metric (which may be MemoryPressureUnknown) is never
consulted here, so an unobserved pressure value cannot bias backend selection.
*/
func (s *scheduler) memoryFits(name string, model ModelConfig) bool {
	if model.MinMemoryGB <= 0 {
		return true
	}
	if name == BackendLocal {
		return s.cfg.CoordinatorNode().MemoryGB >= model.MinMemoryGB
	}
	/*
		Single-node-first guard. A distributed backend tensor-splits the model
		onto worker nodes; if the coordinator can host the whole model alone
		within its safe budget there is no capacity reason to do that, and doing
		so drags a memory-tight worker into the critical path. The 24 GB worker
		kernel-panicked under exactly this over-commit (watchdogd 90 s timeout
		from a jetsam memory-pressure death spiral), so a fit-capable model must
		stay single-node and fall through to the local backend. force_distribute
		is the explicit, unsafe opt-out.
	*/
	if !model.ForceDistribute && s.coordinatorCanHostAlone(model) {
		s.logger.Info("cluster: model fits coordinator alone, preferring single-node over distributing to workers",
			"backend", name, "model", model.ID, "min_memory_gb", model.MinMemoryGB,
			"coordinator_safe_gb", s.coordinatorSafeMemoryGB())
		return false
	}
	return s.mgr.totalClusterMemoryGB() >= model.MinMemoryGB
}

/*
coordinatorSafeMemoryGB is the coordinator memory considered safely usable for a
single model: total memory minus the configured reserve kept free for the OS, KV
cache growth, and Metal compute buffers.
*/
func (s *scheduler) coordinatorSafeMemoryGB() int {
	return s.cfg.CoordinatorNode().MemoryGB - s.cfg.Cluster.CoordinatorReserveGB
}

/* coordinatorCanHostAlone reports whether the model fits the coordinator's safe budget. */
func (s *scheduler) coordinatorCanHostAlone(model ModelConfig) bool {
	return s.coordinatorSafeMemoryGB() >= model.MinMemoryGB
}
