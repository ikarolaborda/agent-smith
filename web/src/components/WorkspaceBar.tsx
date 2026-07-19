import { useEffect, useState } from 'react';
import { authenticatedFetch } from '../auth';

interface WorkspaceState {
  workspace: string;
  writable: boolean;
}

interface TreeEntry {
  path: string;
  dir: boolean;
}

interface TreeResponse {
  workspace: string;
  entries: TreeEntry[];
  truncated: boolean;
}

function basename(p: string): string {
  if (!p) return '';
  const parts = p.replace(/\/+$/, '').split('/');
  return parts[parts.length - 1] || p;
}

/*
 * WorkspaceBar lets the user "open a folder" the way an IDE does: it POSTs an
 * absolute host path to /v1/workspace, which scopes the agent's file_write /
 * file_edit tools to that folder so the model can create and modify files there
 * through chat. The folder lives server-side (the agent runs on the host), so we
 * take a path rather than a browser directory handle. With no folder open the
 * agent stays read-only.
 */
export function WorkspaceBar() {
  const [ws, setWs] = useState<WorkspaceState>({ workspace: '', writable: false });
  const [editing, setEditing] = useState(false);
  const [input, setInput] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [tree, setTree] = useState<TreeResponse | null>(null);
  const [showTree, setShowTree] = useState(false);

  const load = async () => {
    try {
      const res = await authenticatedFetch('/v1/workspace');
      if (res.ok) setWs((await res.json()) as WorkspaceState);
    } catch {
      /* leave the last known state */
    }
  };

  useEffect(() => {
    load();
  }, []);

  const submit = async (path: string) => {
    setBusy(true);
    setError(null);
    try {
      const res = await authenticatedFetch('/v1/workspace', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ path }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data?.error?.message ?? 'could not open folder');
        return;
      }
      setWs(data as WorkspaceState);
      setEditing(false);
      setInput('');
      setShowTree(false);
      setTree(null);
    } catch {
      setError('request failed');
    } finally {
      setBusy(false);
    }
  };

  const toggleTree = async () => {
    if (showTree) {
      setShowTree(false);
      return;
    }
    try {
      const res = await authenticatedFetch('/v1/workspace/tree');
      if (res.ok) setTree((await res.json()) as TreeResponse);
    } catch {
      /* ignore; show nothing */
    }
    setShowTree(true);
  };

  if (!ws.workspace && !editing) {
    return (
      <div className="workspace-bar">
        <button className="workspace-open" onClick={() => setEditing(true)} title="Give the agent a folder to create and edit files in">
          📁 Open folder…
        </button>
      </div>
    );
  }

  if (editing) {
    return (
      <div className="workspace-bar">
        <input
          className="workspace-input"
          autoFocus
          placeholder="/absolute/path/to/folder"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && input.trim()) submit(input.trim());
            if (e.key === 'Escape') {
              setEditing(false);
              setError(null);
            }
          }}
        />
        <button className="workspace-btn" disabled={busy || !input.trim()} onClick={() => submit(input.trim())}>
          Open
        </button>
        <button className="workspace-btn workspace-btn--ghost" onClick={() => { setEditing(false); setError(null); }}>
          Cancel
        </button>
        {error && <span className="workspace-error">{error}</span>}
      </div>
    );
  }

  return (
    <div className="workspace-bar">
      <span className="workspace-chip" title={ws.workspace}>
        📁 {basename(ws.workspace)}
        {ws.writable && <span className="workspace-dot" title="The agent can create and edit files here">✎</span>}
      </span>
      <button className="workspace-btn workspace-btn--ghost" onClick={toggleTree} title="Show files in this folder">
        {showTree ? 'Hide' : 'Files'}
      </button>
      <button className="workspace-btn workspace-btn--ghost" onClick={() => setEditing(true)} title="Open a different folder">
        Change
      </button>
      <button className="workspace-btn workspace-btn--ghost" onClick={() => submit('')} title="Close the folder (agent goes read-only)">
        ✕
      </button>
      {showTree && tree && (
        <div className="workspace-tree">
          {tree.entries.length === 0 && <div className="workspace-tree__empty">(empty)</div>}
          {tree.entries.map((e) => (
            <div key={e.path} className={e.dir ? 'workspace-tree__dir' : 'workspace-tree__file'}>
              {e.dir ? '📂 ' : '📄 '}
              {e.path}
            </div>
          ))}
          {tree.truncated && <div className="workspace-tree__empty">… (truncated)</div>}
        </div>
      )}
    </div>
  );
}
