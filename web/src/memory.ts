export type MemoryKind = 'preference' | 'project_fact' | 'decision' | 'correction';

export interface MemoryChunk {
  id: string;
  kind: MemoryKind;
  text: string;
  importance: number;
  created_at: string;
}

export async function remember(profileId: string, kind: MemoryKind, text: string): Promise<MemoryChunk> {
  const resp = await authenticatedFetch('/v1/rag/remember', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ profile_id: profileId, kind, text }),
  });
  if (!resp.ok) {
    const j = await resp.json().catch(() => ({}));
    throw new Error(j?.error?.message ?? `HTTP ${resp.status}`);
  }
  const json = await resp.json();
  return json.chunk as MemoryChunk;
}

export async function correction(
  profileId: string,
  question: string,
  wrongAnswer: string,
  correctAnswer: string,
): Promise<MemoryChunk> {
  const resp = await authenticatedFetch('/v1/rag/correction', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      profile_id: profileId,
      question,
      wrong_answer: wrongAnswer,
      correct_answer: correctAnswer,
    }),
  });
  if (!resp.ok) {
    const j = await resp.json().catch(() => ({}));
    throw new Error(j?.error?.message ?? `HTTP ${resp.status}`);
  }
  const json = await resp.json();
  return json.chunk as MemoryChunk;
}
import { authenticatedFetch } from './auth';
