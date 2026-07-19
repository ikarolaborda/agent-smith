import { useCallback, useEffect, useMemo, useState } from 'react';
import { Badge, Button, Form, Modal, Spinner } from 'react-bootstrap';
import { authenticatedFetch } from '../auth';
import {
  listResearch,
  researchJSON,
  type Approval,
  type Artifact,
  type AuditEvent,
  type Campaign,
  type CrashGroup,
  type ExperimentRun,
  type Finding,
  type ResearchScope,
} from '../research';

interface Props {
  onUnauthorized: () => void;
  onError: (message: string | null) => void;
}

export function ResearchDashboard({ onUnauthorized, onError }: Props) {
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [scopes, setScopes] = useState<ResearchScope[]>([]);
  const [selectedID, setSelectedID] = useState<string>('');
  const [runs, setRuns] = useState<ExperimentRun[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [groups, setGroups] = useState<CrashGroup[]>([]);
  const [findings, setFindings] = useState<Finding[]>([]);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [showCreate, setShowCreate] = useState(false);
  const [campaignName, setCampaignName] = useState('');
  const [campaignGoal, setCampaignGoal] = useState('');
  const [scopeID, setScopeID] = useState('');
  const [busyApproval, setBusyApproval] = useState<string>('');

  const handleError = useCallback((error: unknown) => {
    const typed = error as Error & { status?: number };
    if (typed.status === 401) onUnauthorized();
    else onError(typed.message || 'Research request failed');
  }, [onError, onUnauthorized]);

  const loadIndex = useCallback(async () => {
    try {
      const [nextCampaigns, nextScopes] = await Promise.all([
        listResearch<Campaign>('/v1/research/campaigns'),
        listResearch<ResearchScope>('/v1/research/scopes'),
      ]);
      setCampaigns(nextCampaigns);
      setScopes(nextScopes);
      setSelectedID((current) => current || nextCampaigns[0]?.id || '');
      setScopeID((current) => current || nextScopes[0]?.id || '');
      onError(null);
    } catch (error) {
      handleError(error);
    } finally {
      setLoading(false);
    }
  }, [handleError, onError]);

  const loadCampaign = useCallback(async (id: string) => {
    if (!id) return;
    try {
      const [nextRuns, nextArtifacts, nextApprovals, nextGroups, nextFindings, nextEvents] = await Promise.all([
        listResearch<ExperimentRun>(`/v1/research/campaigns/${id}/runs`),
        listResearch<Artifact>(`/v1/research/campaigns/${id}/artifacts`),
        listResearch<Approval>(`/v1/research/campaigns/${id}/approvals`),
        listResearch<CrashGroup>(`/v1/research/campaigns/${id}/crash-groups`),
        listResearch<Finding>(`/v1/research/campaigns/${id}/findings`),
        listResearch<AuditEvent>(`/v1/research/events?campaign_id=${encodeURIComponent(id)}&after=0`),
      ]);
      setRuns(nextRuns);
      setArtifacts(nextArtifacts);
      setApprovals(nextApprovals);
      setGroups(nextGroups);
      setFindings(nextFindings);
      setEvents(nextEvents);
    } catch (error) {
      handleError(error);
    }
  }, [handleError]);

  useEffect(() => { void loadIndex(); }, [loadIndex]);
  useEffect(() => {
    void loadCampaign(selectedID);
    if (!selectedID) return;
    const timer = window.setInterval(() => void loadCampaign(selectedID), 5000);
    return () => window.clearInterval(timer);
  }, [loadCampaign, selectedID]);

  const selected = useMemo(() => campaigns.find((campaign) => campaign.id === selectedID), [campaigns, selectedID]);

  async function createCampaign(event: React.FormEvent) {
    event.preventDefault();
    try {
      const campaign = await researchJSON<Campaign>('/v1/research/campaigns', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ scope_id: scopeID, name: campaignName.trim(), goal: campaignGoal.trim() }),
      });
      await researchJSON(`/v1/research/campaigns/${campaign.id}/transition`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ state: 'scoped' }),
      });
      setShowCreate(false);
      setCampaignName('');
      setCampaignGoal('');
      await loadIndex();
      setSelectedID(campaign.id);
    } catch (error) {
      handleError(error);
    }
  }

  async function decideApproval(approval: Approval, approved: boolean) {
    const reason = window.prompt(approved ? 'Approval reason' : 'Denial reason');
    if (!reason?.trim()) return;
    setBusyApproval(approval.id);
    try {
      await researchJSON(`/v1/research/approvals/${approval.id}/decision`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ approved, reason }),
      });
      await loadCampaign(selectedID);
    } catch (error) {
      handleError(error);
    } finally {
      setBusyApproval('');
    }
  }

  async function downloadArtifact(artifact: Artifact) {
    try {
      const response = await authenticatedFetch(`/v1/research/artifacts/${artifact.id}?download=1`);
      if (!response.ok) throw new Error(`Artifact download returned ${response.status}`);
      const url = URL.createObjectURL(await response.blob());
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `${artifact.id}.bin`;
      anchor.click();
      URL.revokeObjectURL(url);
    } catch (error) {
      handleError(error);
    }
  }

  if (loading) return <div className="research-loading"><Spinner size="sm" /> Loading research control plane…</div>;

  return (
    <div className="research-shell">
      <aside className="campaign-rail">
        <div className="campaign-rail-head">
          <div><strong>Campaigns</strong><small>{campaigns.length} durable records</small></div>
          <Button size="sm" onClick={() => setShowCreate(true)} disabled={scopes.length === 0}><i className="bi bi-plus-lg" /></Button>
        </div>
        {campaigns.length === 0 && <p className="research-muted">No campaigns yet. Create an authorization scope through the API first.</p>}
        {campaigns.map((campaign) => (
          <button key={campaign.id} type="button" className={`campaign-item ${campaign.id === selectedID ? 'active' : ''}`} onClick={() => setSelectedID(campaign.id)}>
            <span>{campaign.name}</span><StatusBadge value={campaign.state} />
            <small>{shortID(campaign.id)}</small>
          </button>
        ))}
      </aside>
      <section className="research-main">
        {!selected && <div className="research-empty"><h2>Research control plane</h2><p>Campaign evidence will appear here.</p></div>}
        {selected && (
          <>
            <div className="research-heading">
              <div><span className="eyebrow">Authorized campaign</span><h2>{selected.name}</h2><p>{selected.goal || 'No campaign goal supplied.'}</p></div>
              <div className="research-heading-state"><StatusBadge value={selected.state} /><small>version {selected.version}</small></div>
            </div>
            <div className="metric-grid">
              <Metric label="Runs" value={runs.length} detail={`${runs.filter((run) => run.status === 'running').length} active`} />
              <Metric label="Crash groups" value={groups.length} detail={`${groups.reduce((sum, group) => sum + (group.observation_ids?.length ?? 0), 0)} observations`} />
              <Metric label="Findings" value={findings.length} detail={findings[0]?.label ?? 'none promoted'} />
              <Metric label="Artifacts" value={artifacts.length} detail={formatBytes(artifacts.reduce((sum, artifact) => sum + artifact.size, 0))} />
            </div>
            <div className="research-grid">
              <Panel title="Experiment runs" icon="bi-activity">
                {runs.length === 0 ? <Empty text="No worker runs queued." /> : runs.map((run) => (
                  <div className="research-row" key={run.id}><div><strong>{run.operation}</strong><small>{shortID(run.id)} · {run.isolation_assurance || 'isolation pending'}</small></div><StatusBadge value={run.status} /></div>
                ))}
              </Panel>
              <Panel title="Approvals" icon="bi-person-check">
                {approvals.length === 0 ? <Empty text="No approval decisions." /> : approvals.map((approval) => (
                  <div className="approval-row" key={approval.id}>
                    <div><strong>{approval.operation}</strong><small>{approval.reason}</small></div><StatusBadge value={approval.status} />
                    {approval.status === 'pending' && <div className="approval-actions"><Button size="sm" variant="outline-success" disabled={busyApproval === approval.id} onClick={() => void decideApproval(approval, true)}>Approve</Button><Button size="sm" variant="outline-danger" disabled={busyApproval === approval.id} onClick={() => void decideApproval(approval, false)}>Deny</Button></div>}
                  </div>
                ))}
              </Panel>
              <Panel title="Crash groups & findings" icon="bi-bug">
                {groups.map((group) => <div className="research-row" key={group.id}><div><strong>{group.root_cause || 'Root cause pending'}</strong><small>{group.observation_ids?.length ?? 0} observations · {shortID(group.signature)}</small></div></div>)}
                {findings.map((finding) => <div className="research-row finding-row" key={finding.id}><div><strong>{finding.title}</strong><small>{finding.cwe || 'CWE pending'} · disclosure {finding.disclosure_status || 'not started'}</small></div><StatusBadge value={finding.label} /></div>)}
                {groups.length === 0 && findings.length === 0 && <Empty text="No machine-parsed crash groups or promoted findings." />}
              </Panel>
              <Panel title="Evidence artifacts" icon="bi-shield-lock">
                {artifacts.length === 0 ? <Empty text="No retained artifacts." /> : artifacts.slice(0, 20).map((artifact) => (
                  <div className="research-row" key={artifact.id}><div><strong>{artifact.role}</strong><small>{formatBytes(artifact.size)} · {shortID(artifact.content_id)}</small></div><Button variant="link" size="sm" onClick={() => void downloadArtifact(artifact)} aria-label={`Download ${artifact.id}`}><i className="bi bi-download" /></Button></div>
                ))}
              </Panel>
              <Panel title="Audit timeline" icon="bi-clock-history" wide>
                {events.length === 0 ? <Empty text="No campaign events." /> : events.slice(-25).reverse().map((event) => (
                  <div className="timeline-row" key={event.id}><span className={`timeline-dot ${event.decision ?? ''}`} /><div><strong>{event.action}</strong><small>{event.actor_id} · {new Date(event.created_at).toLocaleString()}</small></div><code>#{event.sequence}</code></div>
                ))}
              </Panel>
            </div>
          </>
        )}
      </section>
      <Modal show={showCreate} onHide={() => setShowCreate(false)} centered>
        <Form onSubmit={createCampaign}>
          <Modal.Header closeButton><Modal.Title>New research campaign</Modal.Title></Modal.Header>
          <Modal.Body>
            <Form.Group className="mb-3"><Form.Label>Authorization scope</Form.Label><Form.Select value={scopeID} onChange={(event) => setScopeID(event.target.value)} required>{scopes.map((scope) => <option key={scope.id} value={scope.id}>{scope.purpose} · {scope.target_repository}</option>)}</Form.Select></Form.Group>
            <Form.Group className="mb-3"><Form.Label>Name</Form.Label><Form.Control value={campaignName} onChange={(event) => setCampaignName(event.target.value)} maxLength={120} required /></Form.Group>
            <Form.Group><Form.Label>Goal</Form.Label><Form.Control as="textarea" rows={3} value={campaignGoal} onChange={(event) => setCampaignGoal(event.target.value)} maxLength={1000} /></Form.Group>
          </Modal.Body>
          <Modal.Footer><Button variant="secondary" onClick={() => setShowCreate(false)}>Cancel</Button><Button type="submit">Create scoped campaign</Button></Modal.Footer>
        </Form>
      </Modal>
    </div>
  );
}

function Panel({ title, icon, wide = false, children }: { title: string; icon: string; wide?: boolean; children: React.ReactNode }) {
  return <section className={`research-panel ${wide ? 'wide' : ''}`}><header><i className={`bi ${icon}`} /><h3>{title}</h3></header><div className="research-panel-body">{children}</div></section>;
}

function Metric({ label, value, detail }: { label: string; value: number; detail: string }) {
  return <div className="metric-card"><span>{label}</span><strong>{value}</strong><small>{detail}</small></div>;
}

function Empty({ text }: { text: string }) { return <p className="research-muted mb-0">{text}</p>; }

function StatusBadge({ value }: { value: string }) {
  const lower = (value || 'unknown').toLowerCase();
  const tone = lower.includes('fail') || lower.includes('denied') || lower.includes('cancel') ? 'danger' : lower.includes('running') || lower.includes('pending') || lower.includes('fuzz') ? 'warning' : lower.includes('complete') || lower.includes('approved') || lower.includes('ready') || lower.includes('scoped') ? 'success' : 'secondary';
  return <Badge bg={tone}>{value || 'unknown'}</Badge>;
}

function shortID(value: string): string { return value.length > 24 ? `${value.slice(0, 12)}…${value.slice(-8)}` : value; }
function formatBytes(value: number): string { if (value < 1024) return `${value} B`; if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`; return `${(value / 1024 ** 2).toFixed(1)} MiB`; }
