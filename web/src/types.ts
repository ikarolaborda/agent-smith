export type Role = 'system' | 'user' | 'assistant' | 'tool';

export interface ToolCall {
  id: string;
  name: string;
  arguments: string;
}

export interface ToolResult {
  tool_call_id: string;
  name: string;
  content: string;
  is_error: boolean;
}

/*
 * ImageAttachment holds a pasted image as a base64 data URL ("data:<mime>;base64,...")
 * so it can be both previewed (<img src>) and sent to the backend unchanged.
 */
export interface ImageAttachment {
  url: string;
}

export interface Message {
  id: string;
  role: Role;
  content: string;
  images?: ImageAttachment[];
  tool_calls?: ToolCall[];
  tool_results?: ToolResult[];
  refine_rounds?: RefineRound[];
  refine_summary?: RefineSummary;
  streaming?: boolean;
}

/*
 * RefineRound is one generate->judge cycle surfaced to the UI so the user can
 * watch the evaluation. status is the judge verdict for that round.
 */
export interface RefineRound {
  iter: number;
  status: 'usable' | 'not_usable';
  usable: boolean;
  reasons?: string;
  fixes?: string[];
  failure_modes?: string[];
  duration_ms: number;
}

/*
 * RefineSummary is the loop outcome. status drives the UI label: only 'usable'
 * may be presented as a confirmed result; 'least_fabricated' is the most honest
 * attempt and MUST be marked NOT confirmed.
 */
export interface RefineSummary {
  status: 'usable' | 'least_fabricated' | 'error';
  usable: boolean;
  reason: string;
  rounds: number;
}

export interface Conversation {
  id: string;
  title: string;
  createdAt: number;
  updatedAt: number;
  provider: string;
  model: string;
  messages: Message[];
  /*
   * Per-conversation override of the server-side web-grounding default.
   * undefined means "use server default" (Ollama=true, cloud=false).
   */
  webSearch?: boolean;
  /*
   * Opt-in judge-in-the-loop refinement for this conversation. undefined/false
   * means a normal single-pass streamed answer.
   */
  refine?: boolean;
}

export interface ProvidersResponse {
  data: { id: string; model: string }[];
  default: string;
}

export interface ModelsResponse {
  data: { id: string; provider: string; model: string; kind?: string; supports_vision?: boolean }[];
}
