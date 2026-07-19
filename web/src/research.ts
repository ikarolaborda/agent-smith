import { authenticatedFetch } from './auth';

export interface ResourceBudget {
  max_wall_seconds: number;
  max_memory_bytes: number;
  max_cpu_seconds: number;
  max_disk_bytes: number;
  max_inodes: number;
  max_pids: number;
  max_concurrent: number;
}

export interface ResearchScope {
  id: string;
  purpose: string;
  target_repository: string;
  operator_id: string;
  expires_at: string;
}

export interface Campaign {
  id: string;
  scope_id: string;
  name: string;
  goal: string;
  state: string;
  target_id?: string;
  version: number;
  budget: ResourceBudget;
  created_at: string;
  updated_at: string;
}

export interface TargetRevision {
  id: string;
  repository: string;
  requested_ref: string;
  commit: string;
  source_sha256: string;
  language: string;
  architecture: string;
	acquisition: {
		method: string;
		source_name?: string;
		source_url?: string;
		bundle_sha256?: string;
		bundle_bytes?: number;
		manifest_key_id?: string;
		manifest_expires_at?: string;
		fetched_at?: string;
	};
  acquired_at: string;
}

export interface ExperimentRun {
  id: string;
  operation: string;
  status: string;
  isolation_assurance?: string;
  artifact_ids?: string[];
  created_at: string;
  completed_at?: string;
  exit?: { code: number; signal?: string; reason?: string };
  resource_usage?: { wall_ms: number; cpu_ms: number; max_rss_bytes: number; disk_written_bytes: number; inodes_created: number };
}

export interface ResearchBuild {
  id: string;
  status: string;
  image_digest: string;
  sanitizer: string;
  architecture: string;
  toolchain?: Record<string, string>;
  provenance?: Record<string, string>;
  output_artifacts?: string[];
}

export interface Artifact {
  id: string;
  content_id: string;
  role: string;
  media_type: string;
  size: number;
  sensitivity: string;
  run_id?: string;
}

export interface Approval {
  id: string;
  operation: string;
  status: string;
  reason: string;
  requested_by: string;
  decided_by?: string;
  created_at: string;
}

export interface CrashGroup {
  id: string;
  signature: string;
  observation_ids: string[];
  minimized_artifact_id?: string;
  root_cause?: string;
}

export interface CrashObservation {
  id: string;
  class: string;
  summary: string;
  signature: string;
  input_artifact_id?: string;
  access?: string;
  access_size?: number;
  security_relevant: boolean;
}

export interface EvidenceValue {
  known: boolean;
  value?: string;
  evidence_ids?: string[];
}

export interface PrimitiveAssessment {
  id: string;
  operation: string;
  operation_evidence_ids: string[];
  attacker_control: EvidenceValue;
  access_width: EvidenceValue;
  value_control: EvidenceValue;
  target_relation: EvidenceValue;
  repeatability: EvidenceValue;
  reachability: EvidenceValue;
  mitigations: EvidenceValue;
  exploitability_gap: EvidenceValue;
}

export interface Finding {
  id: string;
  title: string;
  label: string;
  cwe?: string;
	branch_checks?: GateCheck[];
	novelty_checks?: GateCheck[];
	novelty_status?: string;
	fix_artifact_id?: string;
	regression_run_id?: string;
	report_artifact_id?: string;
	human_review_approval?: string;
	disclosure_approval?: string;
  disclosure_status: string;
	disclosure_reference?: string;
}

export interface GateCheck {
	name: string;
	status: string;
	summary: string;
	evidence_ids?: string[];
	checked_at?: string;
}

export interface RevisionCheck {
	id: string;
	finding_id: string;
	revision: string;
	status: string;
	reason?: string;
	build_id?: string;
	run_id?: string;
	evidence_ids?: string[];
	checked_at: string;
}

export interface SourceEvidence {
	id: string;
	finding_id: string;
	kind: string;
	source_name: string;
	query: string;
	status: string;
	summary: string;
	artifact_id?: string;
	checked_at: string;
}

export interface SourceReview {
	id: string;
	finding_id: string;
	source_evidence_id: string;
	kind: string;
	status: string;
	summary: string;
	reviewed_by: string;
	reviewed_at: string;
}

export interface RemediationValidation {
	id: string;
	finding_id: string;
	patch_artifact_id: string;
	fix_build_id: string;
	reproducer_run_id: string;
	regression_run_id: string;
	negative_control_run_id: string;
	original_signal_gone: boolean;
	regression_passed: boolean;
	negative_control_clean: boolean;
	validated_at: string;
}

export interface AuditEvent {
  id: string;
  sequence: number;
  action: string;
  actor_id: string;
  decision?: string;
  created_at: string;
}

interface ListResponse<T> { data: T[] }

export async function researchJSON<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await authenticatedFetch(path, init);
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const error = new Error(body?.error?.message ?? body?.message ?? `Research API returned ${response.status}`);
    (error as Error & { status?: number }).status = response.status;
    throw error;
  }
  return response.json() as Promise<T>;
}

export function listResearch<T>(path: string): Promise<T[]> {
  return researchJSON<ListResponse<T>>(path).then((response) => response.data ?? []);
}
