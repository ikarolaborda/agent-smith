import { useEffect, useState } from 'react';

interface ClusterNode {
  id: string;
  role: string;
  reachable: boolean;
}

interface ClusterStatus {
  enabled: boolean;
  mode?: string;
  selected_backend?: string;
  model?: string;
  nodes?: ClusterNode[];
  tokens_per_sec?: number;
  requests?: number;
  observed_at?: string;
}

const DISTRIBUTED = ['llama_cpp_rpc', 'exo', 'mlx_jaccl'];

/*
 * Ambient cluster-mode indicator polled from /v1/cluster. It shows the cluster
 * control-plane state and the backend that served the most recent request, so
 * "Clustered" vs "Local fallback" reflects real routing rather than just that a
 * cluster is configured. Renders nothing when the server reports no cluster.
 */
export function ClusterBadge() {
  const [status, setStatus] = useState<ClusterStatus | null>(null);
  const [failures, setFailures] = useState(0);

  useEffect(() => {
    let cancelled = false;
    const poll = async () => {
      try {
        const res = await fetch('/v1/cluster');
        const data = (await res.json()) as ClusterStatus;
        if (!cancelled) {
          setStatus(data);
          setFailures(0);
        }
      } catch {
        if (!cancelled) setFailures((n) => n + 1);
      }
    };
    poll();
    const timer = setInterval(poll, 12000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, []);

  if (!status || !status.enabled || failures > 3) return null;

  const selected = status.selected_backend ?? '';
  const isFallback = selected === 'local';
  const isDistributed = DISTRIBUTED.includes(selected);
  const state = isFallback ? 'fallback' : isDistributed ? 'active' : 'ready';

  const label = !selected
    ? 'Cluster ready'
    : isFallback
      ? 'Local fallback'
      : `Clustered · ${selected}`;

  const nodes = status.nodes ?? [];
  const tooltip = [
    `mode: ${status.mode ?? '?'}`,
    `model: ${status.model ?? '?'}`,
    selected ? `serving on: ${selected}` : 'no request served yet',
    ...nodes.map((n) => `${n.id} (${n.role}): ${n.reachable ? 'reachable' : 'unreachable'}`),
    status.tokens_per_sec ? `${Math.round(status.tokens_per_sec)} tok/s (last request)` : '',
    status.observed_at ? `node status observed ${status.observed_at}` : '',
  ]
    .filter(Boolean)
    .join('\n');

  return (
    <div className={`cluster-badge cluster-badge--${state}`} title={tooltip}>
      <span className="cluster-badge__icon" aria-hidden>
        ⚡
      </span>
      <span className="cluster-badge__label">{label}</span>
      <span className="cluster-badge__nodes">
        {nodes.map((n) => (
          <span
            key={n.id}
            className={`cluster-badge__dot ${n.reachable ? 'is-up' : 'is-down'}`}
            title={`${n.id} (${n.role}): ${n.reachable ? 'reachable' : 'unreachable'}`}
          />
        ))}
      </span>
    </div>
  );
}
