import { authenticatedFetch } from './auth';

export interface ResourceBudget {
  max_wall_seconds: number;
  max_memory_bytes: number;
  max_cpu_seconds: number;
  max_disk_bytes: number;
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
  version: number;
  budget: ResourceBudget;
  created_at: string;
  updated_at: string;
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
  resource_usage?: { wall_ms: number; max_rss_bytes: number };
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

export interface Finding {
  id: string;
  title: string;
  label: string;
  cwe?: string;
  disclosure_status: string;
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
