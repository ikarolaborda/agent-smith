import { authenticatedFetch } from './auth';

/*
 * generateTitle asks the backend to distill a conversation's first user message
 * into a short title using the conversation's own model. It is best-effort: any
 * failure (network, model error, empty result, abort) resolves to null so the
 * caller keeps its instant deriveTitle() placeholder instead of surfacing an
 * error. The optional signal lets the caller cancel a stale request.
 */
export async function generateTitle(
  model: string,
  prompt: string,
  signal?: AbortSignal,
): Promise<string | null> {
  try {
    const resp = await authenticatedFetch('/v1/title', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ model, prompt }),
      signal,
    });
    if (!resp.ok) return null;
    const json = await resp.json();
    const title = typeof json?.title === 'string' ? json.title.trim() : '';
    return title || null;
  } catch {
    return null;
  }
}
