/*
Cluster manager: owns the set of constructed backends, the node topology, and
the lifecycle (discover, start, stop, health). It is the orchestration surface
the provider and CLI use. Backends are constructed lazily-but-eagerly at
construction time (cheap, no process launched until Start) so Probe/Health can
be answered before anything is running.
*/
package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"sync"

	"github.com/ikarolaborda/agent-smith/internal/llm"
)

/* Manager implements ClusterManager. */
type Manager struct {
	cfg     *ClusterConfig
	logger  *slog.Logger
	metrics *Collector

	mu       sync.Mutex
	backends map[string]InferenceBackend
	started  map[string]bool
}

/*
NewManager constructs a Manager. localProvider is the existing in-process
provider used by the local fallback backend; it may be nil only when
runtime.strict_cluster is set (no fallback wanted).
*/
func NewManager(cfg *ClusterConfig, localProvider llm.Provider, logger *slog.Logger, metrics *Collector) *Manager {
	if metrics == nil {
		metrics = NewCollector()
	}
	m := &Manager{
		cfg:      cfg,
		logger:   logger,
		metrics:  metrics,
		backends: map[string]InferenceBackend{},
		started:  map[string]bool{},
	}
	m.backends[BackendExo] = newExoBackend(cfg.Runtime, logger, metrics)
	m.backends[BackendMLXJACCL] = newMLXBackend(cfg.Runtime, logger, metrics)
	m.backends[BackendLlamaRPC] = newLlamaBackend(cfg.Runtime, logger, metrics)
	m.backends[BackendLocal] = newLocalBackend(localProvider, logger, metrics)
	return m
}

/* Metrics exposes the shared collector. */
func (m *Manager) Metrics() *Collector { return m.metrics }

/* backend returns a constructed backend by name, or nil. */
func (m *Manager) backend(name string) InferenceBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.backends[name]
}

/*
Discover probes each configured node for reachability and records it in the
metrics collector. The coordinator (and any loopback host) is always reachable;
remote nodes are probed by TCP-dialing the relevant backend ports. Hosts not on
the allowlist are reported unreachable and never dialed.
*/
func (m *Manager) Discover(ctx context.Context) ([]Node, error) {
	allowed := m.cfg.AllowedHosts()
	coord := m.cfg.CoordinatorNode()
	out := make([]Node, 0, len(m.cfg.Cluster.Nodes))
	for _, n := range m.cfg.Cluster.Nodes {
		reachable, detail := m.probeNode(ctx, n, coord, allowed)
		m.metrics.SetNodeHealth(n.ID, reachable, MemoryPressureUnknown)
		m.logger.Info("cluster: node discovered", "node", n.ID, "host", n.Host, "reachable", reachable, "detail", detail)
		out = append(out, n)
	}
	return out, nil
}

/* probeNode decides whether a node is reachable and why. */
func (m *Manager) probeNode(ctx context.Context, n, coord Node, allowed map[string]bool) (bool, string) {
	if n.ID == coord.ID || isLoopback(n.Host) {
		return true, "coordinator/loopback"
	}
	if !allowed[lowerTrim(n.Host)] {
		return false, "host not on allowlist"
	}
	if m.cfg.Runtime.PrivateClusterOnly && !isPrivateHost(n.Host) {
		return false, "private_cluster_only: host is not a private interface"
	}
	/*
		Do NOT reject the node just because its name ALSO resolves to a public
		address: a multi-homed worker legitimately has a global IPv6 alongside its
		private Thunderbolt/LAN addresses. The security property — only ever DIAL a
		private address under private_cluster_only — is enforced per-candidate by
		resolveRPCAddr (filterPrivateCandidates drops public, then fails closed), so
		a hijacked name resolving to ONLY public addresses still yields no reachable
		candidate. This replaces the old blanket hostResolvesPublic short-circuit.
	*/
	/*
		Probe via resolveRPCAddr, not a raw hostname dial: the worker is
		multi-homed and its rpc-server binds one interface, so dialing the .local
		name can land on a non-listening address and falsely report unreachable.
		resolveRPCAddr resolves the candidates and probes each, matching exactly
		what the llama backend uses to pick the live address at launch — so the
		badge's reachability and the actual --rpc target stay consistent.
	*/
	ports := []int{m.cfg.Runtime.Llama.RPCPort, m.cfg.Runtime.Exo.Port}
	for _, p := range ports {
		hp := net.JoinHostPort(n.Host, strconv.Itoa(p))
		if addr, ok := resolveRPCAddr(ctx, hp, m.cfg.Runtime.PrivateClusterOnly, nodeProbeTimeout); ok {
			return true, "tcp " + addr
		}
	}
	/* DNS resolution alone still means "configured and addressable". */
	if _, err := net.LookupHost(n.Host); err == nil {
		return true, "dns-resolvable (no backend port open yet)"
	}
	return false, "unreachable"
}

/* buildBackendConfig assembles the launch spec for a model. */
func (m *Manager) buildBackendConfig(model ModelConfig) BackendConfig {
	return BackendConfig{
		Model:       model,
		Nodes:       m.cfg.Cluster.Nodes,
		Runtime:     m.cfg.Runtime,
		Coordinator: m.cfg.CoordinatorNode(),
		Workers:     m.cfg.WorkerNodes(),
	}
}

/* StartBackend brings up a named backend for a configured model id. */
func (m *Manager) StartBackend(ctx context.Context, backend string, model string) error {
	mc, ok := m.cfg.ModelByID(model)
	if !ok {
		return fmt.Errorf("cluster: unknown model %q", model)
	}
	return m.ensureStarted(ctx, backend, mc)
}

/*
ensureStarted starts a backend once (idempotent). Concurrent callers serialize
on the manager mutex around the started-flag check, but the Start itself runs
without the lock so a slow process launch does not block Health/metrics reads.
*/
func (m *Manager) ensureStarted(ctx context.Context, backend string, model ModelConfig) error {
	m.mu.Lock()
	b := m.backends[backend]
	already := m.started[backend]
	m.mu.Unlock()
	if b == nil {
		return fmt.Errorf("cluster: unknown backend %q", backend)
	}
	if already {
		return nil
	}
	if backend != BackendLocal {
		m.warnUnreachableWorkers(backend)
	}
	if err := b.Start(ctx, m.buildBackendConfig(model)); err != nil {
		return err
	}
	m.mu.Lock()
	m.started[backend] = true
	m.mu.Unlock()
	return nil
}

/*
warnUnreachableWorkers logs a prominent warning for each worker node that
discovery marked unreachable before a distributed backend starts. v1 expects the
operator to launch worker-side runtimes (exo / rpc-server); this turns the
"worker silently absent" failure mode into an explicit, actionable signal rather
than a quiet single-node degradation.
*/
func (m *Manager) warnUnreachableWorkers(backend string) {
	snap := m.metrics.Snapshot()
	for _, w := range m.cfg.WorkerNodes() {
		if reachable, ok := snap.NodeHealth[w.ID]; ok && !reachable {
			m.logger.Warn("cluster: worker node unreachable; distributed backend will run degraded until it is started/reachable",
				"backend", backend, "worker", w.ID, "host", w.Host)
		}
	}
}

/* StopBackend stops a named backend if it was started. */
func (m *Manager) StopBackend(ctx context.Context, backend string) error {
	m.mu.Lock()
	b := m.backends[backend]
	m.started[backend] = false
	m.mu.Unlock()
	if b == nil {
		return fmt.Errorf("cluster: unknown backend %q", backend)
	}
	return b.Stop(ctx)
}

/* StopAll stops every started backend. Best-effort; first error is returned. */
func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	names := make([]string, 0, len(m.started))
	for name, on := range m.started {
		if on {
			names = append(names, name)
		}
	}
	m.mu.Unlock()
	var firstErr error
	for _, name := range names {
		if err := m.StopBackend(ctx, name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

/* Health aggregates backend + node health and the latest metrics snapshot. */
func (m *Manager) Health(ctx context.Context) (*ClusterHealth, error) {
	snap := m.metrics.Snapshot()
	h := &ClusterHealth{Mode: m.cfg.Cluster.Mode, Selected: snap.Backend, Metrics: snap}

	m.mu.Lock()
	started := map[string]bool{}
	for k, v := range m.started {
		started[k] = v
	}
	m.mu.Unlock()

	for _, name := range []string{BackendExo, BackendMLXJACCL, BackendLlamaRPC, BackendLocal} {
		b := m.backend(name)
		if b == nil {
			continue
		}
		bh, err := b.Health(ctx)
		if err != nil || bh == nil {
			h.Backends = append(h.Backends, BackendHealth{Backend: name, Healthy: false, LastError: errString(err)})
			continue
		}
		h.Backends = append(h.Backends, *bh)
	}
	for _, n := range m.cfg.Cluster.Nodes {
		h.Nodes = append(h.Nodes, NodeHealth{
			ID:             n.ID,
			Host:           n.Host,
			Reachable:      snap.NodeHealth[n.ID],
			MemoryPressure: snap.MemoryPressure[n.ID],
		})
	}
	return h, nil
}

/* totalClusterMemoryGB sums reachable-node unified memory. */
func (m *Manager) totalClusterMemoryGB() int {
	snap := m.metrics.Snapshot()
	total := 0
	for _, n := range m.cfg.Cluster.Nodes {
		/*
			A node counts toward cluster capacity if it has not been probed yet
			(no entry) or was found reachable. An explicitly-unreachable node is
			excluded so the scheduler does not over-promise on a dead worker.
		*/
		if r, ok := snap.NodeHealth[n.ID]; ok && !r {
			continue
		}
		total += n.MemoryGB
	}
	return total
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
