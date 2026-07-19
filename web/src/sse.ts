/*
 * Minimal SSE client. EventSource cannot POST, so we use fetch + ReadableStream
 * and parse the wire format ourselves. The agent-smith server emits:
 *   - default-event `data: {choices:[{delta:{...}}]}` frames (OpenAI shape)
 *   - named `event: tool_result` frames with JSON payloads
 *   - named `event: error` frames with {message}
 *   - terminator `data: [DONE]`
 */

import type { Role, RefineRound, RefineSummary } from './types';
import { authenticatedFetch } from './auth';

/*
 * A wire message sent to /v1/chat/completions. content is either a plain
 * string or the OpenAI multimodal parts array (text + image_url) used to
 * carry pasted images.
 */
export type WireContentPart =
  | { type: 'text'; text: string }
  | { type: 'image_url'; image_url: { url: string } };
export interface WireMessage {
  role: Role;
  content: string | WireContentPart[];
}

export interface OpenAIDelta {
  role?: string;
  content?: string;
  tool_calls?: Array<{
    index?: number;
    id?: string;
    type?: string;
    function?: { name?: string; arguments?: string };
  }>;
}

export interface OpenAIChunk {
  id: string;
  object: string;
  created: number;
  model: string;
  choices: Array<{ index: number; delta: OpenAIDelta; finish_reason?: string | null }>;
}

export interface ToolResultPayload {
  tool_call_id: string;
  name: string;
  content: string;
  is_error: boolean;
}

export interface StreamCallbacks {
  onDelta: (delta: OpenAIDelta) => void;
  onToolResult: (r: ToolResultPayload) => void;
  onError: (message: string) => void;
  onDone: () => void;
  onRefineRound?: (r: RefineRound) => void;
  onRefineSummary?: (s: RefineSummary) => void;
}

export async function streamChatCompletion(
  body: {
    model: string;
    messages: WireMessage[];
    stream: true;
    profile_id?: string;
    web_search?: boolean;
    agentic?: boolean;
    refine?: boolean;
    refine_max_iters?: number;
  },
  cbs: StreamCallbacks,
  signal: AbortSignal,
): Promise<void> {
  const resp = await authenticatedFetch('/v1/chat/completions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Accept: 'text/event-stream' },
    body: JSON.stringify(body),
    signal,
  });

  if (!resp.ok || !resp.body) {
    let detail = `HTTP ${resp.status}`;
    try {
      const j = await resp.json();
      if (j?.error?.message) detail = j.error.message;
    } catch {
      /* ignore */
    }
    cbs.onError(detail);
    cbs.onDone();
    return;
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      /*
       * Normalize CR LF to LF before parsing. The SSE spec accepts CR LF CR LF
       * as a frame terminator, but indexOf('\n\n') would miss it. Some proxies
       * (e.g. nginx with certain encoding paths) emit CRLF on the wire even
       * when the origin used LF. Normalizing once per chunk keeps the rest of
       * the parser simple while staying spec-compliant.
       */
      buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');
      let idx: number;
      while ((idx = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        handleFrame(frame, cbs);
      }
    }
    if (buffer.trim().length > 0) handleFrame(buffer, cbs);
  } catch (err: unknown) {
    if ((err as { name?: string })?.name !== 'AbortError') {
      cbs.onError((err as Error).message ?? String(err));
    }
  } finally {
    cbs.onDone();
  }
}

function handleFrame(frame: string, cbs: StreamCallbacks): void {
  const lines = frame.split(/\r?\n/);
  let eventName = 'message';
  const dataLines: string[] = [];
  for (const line of lines) {
    if (line.startsWith(':') || line === '') continue;
    if (line.startsWith('event:')) {
      eventName = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      dataLines.push(line.slice(5).trimStart());
    }
  }
  if (dataLines.length === 0) return;
  const data = dataLines.join('\n');

  if (data === '[DONE]') {
    cbs.onDone();
    return;
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(data);
  } catch {
    return;
  }

  if (eventName === 'tool_result') {
    cbs.onToolResult(parsed as ToolResultPayload);
    return;
  }
  if (eventName === 'refine_round') {
    cbs.onRefineRound?.(parsed as RefineRound);
    return;
  }
  if (eventName === 'refine_summary') {
    cbs.onRefineSummary?.(parsed as RefineSummary);
    return;
  }
  if (eventName === 'error') {
    cbs.onError((parsed as { message?: string }).message ?? 'unknown error');
    return;
  }

  /* default message event = OpenAI chunk */
  const chunk = parsed as OpenAIChunk;
  const choice = chunk.choices?.[0];
  if (choice?.delta) cbs.onDelta(choice.delta);
}
