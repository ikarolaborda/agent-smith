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

export interface Message {
  id: string;
  role: Role;
  content: string;
  tool_calls?: ToolCall[];
  tool_results?: ToolResult[];
  streaming?: boolean;
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
}

export interface ProvidersResponse {
  data: { id: string; model: string }[];
  default: string;
}

export interface ModelsResponse {
  data: { id: string; provider: string; model: string; kind?: string }[];
}
