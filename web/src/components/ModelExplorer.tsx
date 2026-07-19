import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { Modal, Button, Form, Spinner } from 'react-bootstrap';
import { authenticatedFetch } from '../auth';

type GPU = {
  vendor?: string;
  name?: string;
  vram_bytes: number;
  backend: string;
  unified: boolean;
};

type Host = {
  os: string;
  arch: string;
  total_memory_bytes: number;
  available_memory_bytes: number;
  free_disk_bytes: number;
  gpu: GPU;
};

type HFModel = {
  id: string;
  downloads: number;
  likes: number;
  gguf: boolean;
  tags?: string[];
};

function gib(bytes: number): string {
  if (!bytes) return '—';
  return (bytes / 1024 / 1024 / 1024).toFixed(1) + ' GiB';
}

function gpuLine(g?: GPU): string {
  if (!g || g.backend === 'none' || !g.backend) {
    return 'none detected — CPU inference';
  }
  const name = g.name || g.vendor || 'GPU';
  const vram = g.vram_bytes ? ` · ${gib(g.vram_bytes)} VRAM` : '';
  const unified = g.unified ? ' (unified memory)' : '';
  return `${name}${vram} · ${g.backend}${unified}`;
}

export function ModelExplorer({ show, onClose }: { show: boolean; onClose: () => void }) {
  const [host, setHost] = useState<Host | null>(null);
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<HFModel[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!show) return;
    setHost(null);
    authenticatedFetch('/v1/system')
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error('system probe failed'))))
      .then((d) => setHost(d.host as Host))
      .catch(() => setHost(null));
  }, [show]);

  async function search(e?: FormEvent) {
    e?.preventDefault();
    if (!query.trim()) return;
    setLoading(true);
    setError(null);
    try {
      const r = await authenticatedFetch('/v1/models/search?q=' + encodeURIComponent(query.trim()));
      if (!r.ok) throw new Error('search unavailable (is the machine online?)');
      const d = await r.json();
      setResults((d.data as HFModel[]) || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'search failed');
      setResults([]);
    } finally {
      setLoading(false);
    }
  }

  return (
    <Modal show={show} onHide={onClose} size="lg" scrollable>
      <Modal.Header closeButton>
        <Modal.Title>Models &amp; system</Modal.Title>
      </Modal.Header>
      <Modal.Body>
        <div className="system-card">
          <h6 className="mb-2">Detected hardware</h6>
          {host ? (
            <ul className="system-facts">
              <li>
                <span className="k">OS</span> {host.os}/{host.arch}
              </li>
              <li>
                <span className="k">RAM</span> {gib(host.total_memory_bytes)} total · {gib(host.available_memory_bytes)} available
              </li>
              <li>
                <span className="k">Disk</span> {gib(host.free_disk_bytes)} free
              </li>
              <li>
                <span className="k">GPU</span> {gpuLine(host.gpu)}
              </li>
            </ul>
          ) : (
            <Spinner animation="border" size="sm" />
          )}
          <p className="text-muted small mb-0">
            Leave <code>gpu_layers</code> and <code>ctx_size</code> unset in the config and the app auto-tunes them to
            this hardware on launch — full GPU offload with a generous context when the accelerator has room.
          </p>
        </div>

        <hr />

        <Form onSubmit={search} className="d-flex gap-2 mb-2">
          <Form.Control
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search Hugging Face GGUF models (qwen, llama, gemma, abliterated…)"
            aria-label="Search Hugging Face GGUF models"
          />
          <Button type="submit" disabled={loading || !query.trim()}>
            {loading ? <Spinner size="sm" animation="border" /> : 'Search'}
          </Button>
        </Form>
        {error && <div className="text-danger small mb-2">{error}</div>}

        <ul className="model-results">
          {results.map((m) => (
            <li key={m.id}>
              <a href={`https://huggingface.co/${m.id}`} target="_blank" rel="noreferrer">
                {m.id}
              </a>
              <span className="text-muted small">
                {' · '}
                {m.downloads.toLocaleString()} downloads
                {m.gguf ? ' · GGUF' : ''}
              </span>
            </li>
          ))}
          {results.length === 0 && !loading && (
            <li className="text-muted small">Search to browse downloadable GGUF models.</li>
          )}
        </ul>

        <p className="text-muted small mb-0">
          To run one, point the <code>llamacpp</code> provider’s <code>repo</code> at it (or{' '}
          <code>./bin/agent --pull hf.co/&lt;id&gt;:Q4_K_M</code>). One-click download &amp; run from here is the next step.
        </p>
      </Modal.Body>
    </Modal>
  );
}
