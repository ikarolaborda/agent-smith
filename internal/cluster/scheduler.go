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
	"strconv"
	"strings"

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
	/*
		Fallback is still a launch decision. It must pass the same admission gate;
		otherwise a failed distributed start can silently move the entire model
		onto the coordinator and create the OOM/kernel-pressure event clustering
		was meant to prevent.
	*/
	if !s.memoryFits(BackendLocal, model) {
		return nil, fmt.Errorf("cluster: no backend available and local fallback is unsafe: model %q needs %d GB, coordinator safe budget is %d GB",
			model.ID, model.MinMemoryGB, s.coordinatorSafeMemoryGB())
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
backend can draw on: the whole reachable cluster for distributed backends, or
the coordinator's safe budget (total minus its OS/runtime reserve) for local.
Validation rejects zero/unknown estimates before the scheduler is constructed.

Placement uses only the statically configured memory_gb of reachable nodes; the
runtime memory-pressure metric (which may be MemoryPressureUnknown) is never
consulted here, so an unobserved pressure value cannot bias backend selection.
*/
func (s *scheduler) memoryFits(name string, model ModelConfig) bool {
	if model.MinMemoryGB <= 0 {
		/* Defense in depth for programmatically constructed, unvalidated config. */
		return false
	}
	if name == BackendLocal {
		return s.coordinatorSafeMemoryGB() >= model.MinMemoryGB
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
	if s.mgr.totalClusterMemoryGB() < model.MinMemoryGB {
		return false
	}
	/*
		Hard per-node safety budget. Distributing weights onto a worker also drags
		a compute-graph buffer onto it that scales with CONTEXT, not the weight
		share — a 24 GB worker froze at 18.6 GB holding only a 0.10 weight slice at
		32k ctx. This refuses (falls back to local) when any worker's projected
		peak exceeds its safe budget, so the worker participates only when it fits.
		This applies EVEN under force_distribute: force_distribute waives the
		single-node-first preference, never the safety limit.
	*/
	if !s.workersWithinSafeBudget(model) {
		return false
	}
	return true
}

/*
workersWithinSafeBudget reports whether every worker node's projected peak memory
for this model stays inside its safe budget. Projected peak = weight slice +
compute/KV reserve that scales with context. Logs and returns false on the first
worker that would overflow.
*/
func (s *scheduler) workersWithinSafeBudget(model ModelConfig) bool {
	share := s.workerTensorShare()
	reserve := computeReserveGB(model.ContextTokens)
	for _, w := range s.cfg.WorkerNodes() {
		budget := s.nodeSafeModelGB(w)
		projected := int(float64(model.MinMemoryGB)*share+0.5) + reserve
		if projected > budget {
			s.logger.Warn("cluster: worker slice exceeds its safe memory budget, refusing distributed launch (use a smaller context or single-node)",
				"worker", w.ID, "projected_peak_gb", projected, "safe_budget_gb", budget,
				"weight_share", share, "context_tokens", model.ContextTokens, "compute_reserve_gb", reserve)
			return false
		}
	}
	return true
}

/* nodeSafeModelGB is the node's hard cap on model-related memory; zero derives to half its RAM. */
func (s *scheduler) nodeSafeModelGB(n Node) int {
	if n.SafeModelGB > 0 {
		return n.SafeModelGB
	}
	return n.MemoryGB / 2
}

/*
workerTensorShare is the fraction of the model assigned to the worker (the last
tensor_split value; the coordinator is device 0). Defaults to the worker's share
of total cluster RAM when no split is configured.
*/
func (s *scheduler) workerTensorShare() float64 {
	ts := strings.TrimSpace(s.cfg.Runtime.Llama.TensorSplit)
	if ts != "" {
		parts := strings.Split(ts, ",")
		if v, err := strconv.ParseFloat(strings.TrimSpace(parts[len(parts)-1]), 64); err == nil && v > 0 {
			return v
		}
	}
	total := s.mgr.totalClusterMemoryGB()
	workerMem := 0
	for _, w := range s.cfg.WorkerNodes() {
		workerMem += w.MemoryGB
	}
	if total > 0 && workerMem > 0 {
		return float64(workerMem) / float64(total)
	}
	return 0.5
}

/*
computeReserveGB estimates the non-weight memory (KV slice + Metal compute-graph
buffer) a worker must hold, which grows with context. Empirically ~16 GB at 32k
on the M5 Pro, so ~ctx/2048 GB, floored at 2. Conservative on purpose: the
compute buffer cannot be measured before load, and the failure mode is an
unrecoverable freeze, so the guard budgets against the peak.
*/
func computeReserveGB(contextTokens int) int {
	if contextTokens <= 0 {
		contextTokens = 4096
	}
	reserve := contextTokens / 2048
	if reserve < 2 {
		reserve = 2
	}
	return reserve
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
