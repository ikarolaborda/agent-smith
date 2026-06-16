/*
Cluster status for the UI. ClusterStatus is the JSON-safe, intentionally minimal
view the web app polls (via the server's /v1/cluster route) to show that chat is
running clustered and on which backend. It deliberately omits node hostnames/IPs
(only id/role/reachable) so cluster topology does not leak into screenshots or
logs, and it carries observed_at so the UI can show how fresh the node view is.
*/
package cluster

import (
	"context"
	"time"
)

/* nodeHealthRefreshInterval is how often the background loop re-probes nodes. */
const nodeHealthRefreshInterval = 20 * time.Second

/* nodeProbeTimeout bounds resolving+probing one node's address during discovery. */
const nodeProbeTimeout = 1500 * time.Millisecond

/* ClusterStatus is the JSON payload returned by /v1/cluster. */
type ClusterStatus struct {
	Enabled         bool                   `json:"enabled"`
	Mode            string                 `json:"mode"`
	SelectedBackend string                 `json:"selected_backend"`
	Model           string                 `json:"model"`
	Nodes           []ClusterNodeStatus    `json:"nodes"`
	Backends        []ClusterBackendStatus `json:"backends"`
	TokensPerSec    float64                `json:"tokens_per_sec"`
	Requests        int                    `json:"requests"`
	/* ObservedAt is when the served metrics/health snapshot was last updated. */
	ObservedAt string `json:"observed_at,omitempty"`
}

/* ClusterNodeStatus is the sanitized per-node view (no host/IP). */
type ClusterNodeStatus struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	Reachable bool   `json:"reachable"`
}

/* ClusterBackendStatus is the per-backend health view. */
type ClusterBackendStatus struct {
	Backend string `json:"backend"`
	Healthy bool   `json:"healthy"`
}

/*
Status translates the live cluster health + metrics into the UI DTO.
SelectedBackend reflects the backend that served the most recent request (empty
until the first chat), so the UI can distinguish a real clustered turn from a
local fallback. Node reachability comes from the background refresh, so it tracks
a worker dropping rather than only the startup probe.
*/
func (p *Provider) Status(ctx context.Context) *ClusterStatus {
	st := &ClusterStatus{Enabled: true, Mode: p.cfg.Cluster.Mode}
	if len(p.cfg.Models) > 0 {
		st.Model = p.cfg.Models[0].ServedName
		if st.Model == "" {
			st.Model = p.cfg.Models[0].ID
		}
	}
	roles := make(map[string]string, len(p.cfg.Cluster.Nodes))
	for _, n := range p.cfg.Cluster.Nodes {
		roles[n.ID] = n.Role
	}
	h, err := p.mgr.Health(ctx)
	if err != nil || h == nil {
		return st
	}
	st.SelectedBackend = h.Selected
	st.TokensPerSec = h.Metrics.TokensPerSecond
	st.Requests = h.Metrics.Requests
	if !h.Metrics.UpdatedAt.IsZero() {
		st.ObservedAt = h.Metrics.UpdatedAt.UTC().Format(time.RFC3339)
	}
	for _, n := range h.Nodes {
		st.Nodes = append(st.Nodes, ClusterNodeStatus{ID: n.ID, Role: roles[n.ID], Reachable: n.Reachable})
	}
	for _, b := range h.Backends {
		st.Backends = append(st.Backends, ClusterBackendStatus{Backend: b.Backend, Healthy: b.Healthy})
	}
	return st
}

/*
healthRefreshLoop re-runs node discovery on an interval so cached node health
stays current for the UI without coupling network probes to each UI poll. Each
sweep is bounded so an unreachable worker cannot stall the loop.
*/
func (m *Manager) healthRefreshLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, _ = m.Discover(probeCtx)
			cancel()
		}
	}
}
