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
  type CrashObservation,
  type CrashGroup,
  type ExperimentRun,
  type Finding,
  type PrimitiveAssessment,
  type ResearchBuild,
  type ResearchScope,
	type RemediationValidation,
	type RevisionCheck,
	type SourceEvidence,
	type SourceReview,
  type TargetRevision,
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
  const [builds, setBuilds] = useState<ResearchBuild[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [groups, setGroups] = useState<CrashGroup[]>([]);
  const [observations, setObservations] = useState<CrashObservation[]>([]);
  const [primitives, setPrimitives] = useState<PrimitiveAssessment[]>([]);
  const [findings, setFindings] = useState<Finding[]>([]);
	const [revisionChecks, setRevisionChecks] = useState<RevisionCheck[]>([]);
	const [sourceEvidence, setSourceEvidence] = useState<SourceEvidence[]>([]);
	const [sourceReviews, setSourceReviews] = useState<SourceReview[]>([]);
	const [remediations, setRemediations] = useState<RemediationValidation[]>([]);
  const [target, setTarget] = useState<TargetRevision | null>(null);
	const [targets, setTargets] = useState<TargetRevision[]>([]);
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
	  const campaign = await researchJSON<Campaign>(`/v1/research/campaigns/${id}`);
	  const [nextRuns, nextBuilds, nextArtifacts, nextApprovals, nextGroups, nextObservations, nextPrimitives, nextFindings, nextRevisionChecks, nextSourceEvidence, nextSourceReviews, nextRemediations, nextTargets, nextEvents] = await Promise.all([
        listResearch<ExperimentRun>(`/v1/research/campaigns/${id}/runs`),
        listResearch<ResearchBuild>(`/v1/research/campaigns/${id}/builds`),
        listResearch<Artifact>(`/v1/research/campaigns/${id}/artifacts`),
        listResearch<Approval>(`/v1/research/campaigns/${id}/approvals`),
        listResearch<CrashGroup>(`/v1/research/campaigns/${id}/crash-groups`),
        listResearch<CrashObservation>(`/v1/research/campaigns/${id}/crash-observations`),
        listResearch<PrimitiveAssessment>(`/v1/research/campaigns/${id}/primitive-assessments`),
        listResearch<Finding>(`/v1/research/campaigns/${id}/findings`),
		listResearch<RevisionCheck>(`/v1/research/campaigns/${id}/revision-checks`),
		listResearch<SourceEvidence>(`/v1/research/campaigns/${id}/source-evidence`),
		listResearch<SourceReview>(`/v1/research/campaigns/${id}/source-reviews`),
		listResearch<RemediationValidation>(`/v1/research/campaigns/${id}/remediations`),
		listResearch<TargetRevision>(`/v1/research/campaigns/${id}/targets`),
        listResearch<AuditEvent>(`/v1/research/events?campaign_id=${encodeURIComponent(id)}&after=0`),
      ]);
      setRuns(nextRuns);
      setBuilds(nextBuilds);
      setArtifacts(nextArtifacts);
      setApprovals(nextApprovals);
      setGroups(nextGroups);
      setObservations(nextObservations);
      setPrimitives(nextPrimitives);
      setFindings(nextFindings);
	  setRevisionChecks(nextRevisionChecks);
	  setSourceEvidence(nextSourceEvidence);
	  setSourceReviews(nextSourceReviews);
	  setRemediations(nextRemediations);
	  setTargets(nextTargets);
      setEvents(nextEvents);
	  setCampaigns((current) => current.map((item) => item.id === campaign.id ? campaign : item));
	  setTarget(campaign.target_id ? await researchJSON<TargetRevision>(`/v1/research/campaigns/${id}/target`) : null);
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
  const selectedScope = useMemo(() => scopes.find((scope) => scope.id === selected?.scope_id), [scopes, selected]);

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

  async function cancelRun(run: ExperimentRun) {
    try {
      await researchJSON(`/v1/research/runs/${run.id}/cancel`, { method: 'POST' });
      await loadCampaign(selectedID);
    } catch (error) {
      handleError(error);
    }
  }

	async function workflowPost(path: string, body: Record<string, unknown>) {
		try {
			await researchJSON(`/v1/research/campaigns/${selectedID}/${path}`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
			await loadCampaign(selectedID);
		} catch (error) {
			handleError(error);
		}
	}

	function firstFinding(): Finding | null {
		if (!findings[0]) handleError(new Error('A promoted finding is required first.'));
		return findings[0] ?? null;
	}

	async function recordUntestedRevision() {
		const finding = firstFinding(); if (!finding) return;
		const revision = window.prompt('Authorized revision'); if (!revision?.trim()) return;
		const reason = window.prompt('Explicit reason this revision could not be tested'); if (!reason?.trim()) return;
		await workflowPost('revision-checks', { finding_id: finding.id, revision, reason });
	}

	async function captureComparisonTarget() {
		const finding = firstFinding(); if (!finding) return;
		const revision = window.prompt('Authorized supported revision'); if (!revision?.trim()) return;
		const sourceDir = window.prompt('Existing local source snapshot directory'); if (!sourceDir?.trim()) return;
		const language = window.prompt('Language', target?.language || 'c++'); if (!language?.trim()) return;
		const architecture = window.prompt('Architecture', target?.architecture || 'amd64'); if (!architecture?.trim()) return;
		const approvalID = window.prompt(`Acquisition approval ID if required (correlation target:${finding.id}:${revision.trim()})`) ?? '';
		await workflowPost('targets', { finding_id: finding.id, repository: selectedScope?.target_repository || target?.repository, revision, source_dir: sourceDir, language, architecture, approval_id: approvalID, correlation_id: `target:${finding.id}:${revision.trim()}` });
	}

	async function completeBranchReview() {
		const finding = firstFinding(); if (finding) await workflowPost('branch-review', { finding_id: finding.id });
	}

	async function runLookup() {
		const finding = firstFinding(); if (!finding) return;
		const sourceName = window.prompt('Configured fixed source name (for example nvd)'); if (!sourceName?.trim()) return;
		const query = window.prompt('Bounded lookup query'); if (!query?.trim()) return;
		const approvalID = window.prompt(`Approval ID if required (correlation lookup:${finding.id}:${sourceName.trim()})`) ?? '';
		await workflowPost('lookups', { finding_id: finding.id, source_name: sourceName, query, approval_id: approvalID });
	}

	async function reviewLookup(evidence: SourceEvidence) {
		const status = window.prompt('Review status: match, no_match, unavailable, or error'); if (!status?.trim()) return;
		const summary = window.prompt('Evidence-bounded review summary'); if (!summary?.trim()) return;
		await workflowPost('source-reviews', { finding_id: evidence.finding_id, source_evidence_id: evidence.id, status, summary });
	}

	async function completeNoveltyReview() {
		const finding = firstFinding(); if (finding) await workflowPost('novelty-review', { finding_id: finding.id });
	}

	async function createPatch() {
		const finding = firstFinding(); if (!finding) return;
		const approvalID = window.prompt(`Regression approval ID (correlation patch:${finding.id})`); if (!approvalID?.trim()) return;
		const diff = window.prompt('Paste a bounded textual unified diff'); if (!diff?.trim()) return;
		await workflowPost('candidate-patches', { finding_id: finding.id, approval_id: approvalID, diff });
	}

	async function validateFix() {
		const finding = firstFinding(); if (!finding) return;
		const patchArtifactID = window.prompt('Candidate patch artifact ID'); if (!patchArtifactID?.trim()) return;
		const approvalID = window.prompt(`Regression approval ID (correlation patch:${finding.id})`); if (!approvalID?.trim()) return;
		const fixBuildID = window.prompt('Patch-linked fix build ID'); if (!fixBuildID?.trim()) return;
		const reproducerRunID = window.prompt('Original reproducer validation run ID'); if (!reproducerRunID?.trim()) return;
		const regressionRunID = window.prompt('Regression corpus validation run ID'); if (!regressionRunID?.trim()) return;
		const negativeControlRunID = window.prompt('Negative-control validation run ID'); if (!negativeControlRunID?.trim()) return;
		await workflowPost('remediations', { finding_id: finding.id, patch_artifact_id: patchArtifactID, approval_id: approvalID, fix_build_id: fixBuildID, reproducer_run_id: reproducerRunID, regression_run_id: regressionRunID, negative_control_run_id: negativeControlRunID });
	}

	async function createReport() {
		const finding = firstFinding(); if (!finding) return;
		const approvalID = window.prompt(`Report approval ID (correlation report:${finding.id})`); if (!approvalID?.trim()) return;
		await workflowPost('reports', { finding_id: finding.id, approval_id: approvalID });
	}

	async function recordDisclosure() {
		const finding = firstFinding(); if (!finding) return;
		const approvalID = window.prompt(`Disclosure approval ID (correlation disclosure:${finding.id})`); if (!approvalID?.trim()) return;
		const reference = window.prompt('Human disclosure reference (ticket, case, or message ID)'); if (!reference?.trim()) return;
		await workflowPost('disclosures', { finding_id: finding.id, approval_id: approvalID, reference });
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
			<div className="scope-banner">
			  <div><strong>{selectedScope?.target_repository ?? 'Scope unavailable'}</strong><small>authorization expires {selectedScope ? new Date(selectedScope.expires_at).toLocaleString() : 'unknown'}</small></div>
			  <StatusBadge value={new Date(selectedScope?.expires_at ?? 0) > new Date() ? 'scope active' : 'scope expired'} />
			</div>
            <div className="metric-grid">
              <Metric label="Runs" value={runs.length} detail={`${runs.filter((run) => run.status === 'running').length} active`} />
              <Metric label="Builds" value={builds.length} detail={builds[0]?.sanitizer ?? 'none retained'} />
              <Metric label="Crash groups" value={groups.length} detail={`${groups.reduce((sum, group) => sum + (group.observation_ids?.length ?? 0), 0)} observations`} />
              <Metric label="Findings" value={findings.length} detail={findings[0]?.label ?? 'none promoted'} />
              <Metric label="Artifacts" value={artifacts.length} detail={formatBytes(artifacts.reduce((sum, artifact) => sum + artifact.size, 0))} />
            </div>
            <div className="research-grid">
              <Panel title="Experiment runs" icon="bi-activity">
                {runs.length === 0 ? <Empty text="No worker runs queued." /> : runs.map((run) => (
                  <div className="research-row" key={run.id}><div><strong>{run.operation}</strong><small>{shortID(run.id)} · {run.isolation_assurance || 'isolation pending'}{run.resource_usage ? ` · ${formatUsage(run.resource_usage)}` : ''}</small></div><StatusBadge value={run.status} />{(run.status === 'queued' || run.status === 'running') && <Button variant="link" size="sm" onClick={() => void cancelRun(run)}>Cancel</Button>}</div>
                ))}
              </Panel>
              <Panel title="Build provenance" icon="bi-box-seam">
                {builds.length === 0 ? <Empty text="No evidence-backed builds." /> : builds.map((build) => (
                  <div className="research-row" key={build.id}><div><strong>{build.provenance?.harness || shortID(build.id)}</strong><small>{build.toolchain?.compiler || 'compiler pending'} · {build.sanitizer} · {shortID(build.image_digest)}</small></div><StatusBadge value={build.status} /></div>
                ))}
              </Panel>
			  <Panel title="Target provenance" icon="bi-git">
				{!target ? <Empty text="No immutable target has been acquired." /> : <div className="target-provenance"><strong>{target.repository}</strong><small>{target.commit} · {target.language}/{target.architecture}</small><code>{target.source_sha256}</code></div>}
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
                {observations.map((observation) => <div className="research-row" key={observation.id}><div><strong>{observation.summary}</strong><small>{observation.class} · {observation.access || 'effect unknown'}{observation.access_size ? ` ${observation.access_size} byte` : ''}</small></div><StatusBadge value={observation.security_relevant ? 'evidence' : 'non-security'} /></div>)}
                {findings.map((finding) => <div className="research-row finding-row" key={finding.id}><div><strong>{finding.title}</strong><small>{finding.cwe || 'CWE pending'} · disclosure {finding.disclosure_status || 'not started'}</small></div><StatusBadge value={finding.label} /></div>)}
                {groups.length === 0 && findings.length === 0 && <Empty text="No machine-parsed crash groups or promoted findings." />}
              </Panel>
			  <Panel title="Branch & novelty gates" icon="bi-signpost-split" wide>
				<div className="workflow-actions"><Button size="sm" variant="outline-secondary" onClick={() => void captureComparisonTarget()}>Capture revision</Button><Button size="sm" variant="outline-secondary" onClick={() => void recordUntestedRevision()}>Record untested</Button><Button size="sm" variant="outline-primary" onClick={() => void completeBranchReview()}>Complete branch review</Button><Button size="sm" variant="outline-secondary" onClick={() => void runLookup()}>Run fixed lookup</Button><Button size="sm" variant="outline-primary" onClick={() => void completeNoveltyReview()}>Complete novelty review</Button></div>
				{targets.map((item) => <div className="research-row" key={`target-${item.id}`}><div><strong>{item.commit}</strong><small>{shortID(item.source_sha256)} · {item.language}/{item.architecture}</small></div><StatusBadge value={item.id === selected.target_id ? 'primary' : 'comparison'} /></div>)}
				{revisionChecks.map((check) => <div className="research-row" key={check.id}><div><strong>{check.revision}</strong><small>{check.reason || `${shortID(check.build_id ?? '')} · ${shortID(check.run_id ?? '')}`}</small></div><StatusBadge value={check.status} /></div>)}
				{sourceEvidence.map((evidence) => <div className="research-row" key={evidence.id}><div><strong>{evidence.kind} · {evidence.source_name}</strong><small>{evidence.summary || evidence.query} · {sourceReviews.filter((review) => review.source_evidence_id === evidence.id).length} review(s)</small></div><StatusBadge value={evidence.status} /><Button size="sm" variant="link" onClick={() => void reviewLookup(evidence)}>Review</Button></div>)}
				{revisionChecks.length === 0 && sourceEvidence.length === 0 && <Empty text="No supported-revision or retained lookup evidence yet." />}
			  </Panel>
			  <Panel title="Remediation & disclosure" icon="bi-shield-check" wide>
				<div className="workflow-actions"><Button size="sm" variant="outline-secondary" onClick={() => void createPatch()}>Retain candidate patch</Button><Button size="sm" variant="outline-primary" onClick={() => void validateFix()}>Validate remediation</Button><Button size="sm" variant="outline-primary" onClick={() => void createReport()}>Create private report</Button><Button size="sm" variant="outline-danger" onClick={() => void recordDisclosure()}>Record human disclosure</Button></div>
				{findings.map((finding) => <div className="research-row" key={`workflow-${finding.id}`}><div><strong>{finding.novelty_status || 'novelty pending'}</strong><small>patch {shortID(finding.fix_artifact_id || 'none')} · report {shortID(finding.report_artifact_id || 'none')} · {finding.disclosure_reference || 'not disclosed'}</small></div><StatusBadge value={finding.disclosure_status || 'not_disclosed'} /></div>)}
				{remediations.map((validation) => <div className="research-row" key={validation.id}><div><strong>Validated fix {shortID(validation.fix_build_id)}</strong><small>reproducer clean · regression passed · negative control clean</small></div><StatusBadge value={validation.original_signal_gone && validation.regression_passed && validation.negative_control_clean ? 'validated' : 'failed'} /></div>)}
			  </Panel>
              <Panel title="Primitive evidence matrix" icon="bi-grid-3x3-gap" wide>
                {primitives.length === 0 ? <Empty text="No primitive assessment yet; all exploitability dimensions remain unknown." /> : primitives.map((primitive) => (
                  <div className="primitive-matrix" key={primitive.id}>
                    <strong>{primitive.operation}</strong>
                    {Object.entries(primitive).filter(([key]) => !['id', 'operation', 'operation_evidence_ids'].includes(key)).map(([key, value]) => {
                      const evidence = value as { known?: boolean; value?: string; evidence_ids?: string[] };
                      return <div key={key}><small>{key.replaceAll('_', ' ')}</small><span>{evidence.known ? evidence.value : 'unknown'}</span><code>{evidence.evidence_ids?.length ?? 0} evidence</code></div>;
                    })}
                  </div>
                ))}
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
function formatUsage(value: NonNullable<ExperimentRun['resource_usage']>): string { return `${value.wall_ms} ms · ${formatBytes(value.disk_written_bytes || 0)} writable Δ · ${value.inodes_created || 0} inode Δ`; }
