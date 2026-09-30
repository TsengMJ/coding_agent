// Mirrors of the Go JSON types served by code/server and code/trace.

export type Role = "system" | "user" | "assistant" | "tool";

export interface ContentBlock {
  type: string;
  text?: string;
  mime_type?: string;
}

export interface ToolCall {
  id: string;
  name: string;
  args: Record<string, unknown>;
}

export interface Usage {
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  cache_read?: number;
  cache_write?: number;
}

export interface Message {
  id?: string;
  role: Role;
  name?: string;
  content: ContentBlock[] | null;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  status?: "success" | "error";
  usage?: Usage;
  metadata?: Record<string, unknown>;
}

export interface ActionRequest {
  name: string;
  args: Record<string, unknown>;
  description?: string;
  allowed_decisions?: string[];
}

export interface Interrupt {
  id: string;
  action_requests: ActionRequest[];
}

export interface Decision {
  type: "approve" | "reject" | "edit" | "respond";
  message?: string;
  edited_args?: Record<string, unknown>;
}

export interface LLMCall {
  seq: number;
  namespace?: string[];
  model?: string;
  start: string;
  duration_ms: number;
  message_count: number;
  new_from: number;
  tool_count: number;
  usage?: Usage;
  response_text?: string;
  tool_calls?: string[];
  error?: string;
}

export interface ToolSpec {
  name: string;
  description: string;
  parameters: Record<string, unknown>;
}

export interface LLMCallDetail extends LLMCall {
  messages: Message[];
  tools?: ToolSpec[];
  tool_choice?: string;
  settings?: Record<string, unknown>;
  response?: Message;
}

export interface FileDiff {
  tool_call_id?: string;
  namespace?: string[];
  path: string;
  op: "write" | "edit" | "delete";
  unified: string;
  added: number;
  removed: number;
  time: string;
}

export interface TraceMessage {
  namespace: string[];
  message: Message;
}

export interface Trace {
  thread_id: string;
  llm_calls: LLMCall[];
  diffs: FileDiff[];
  messages: TraceMessage[];
}

export interface TraceUpdate {
  llm_call?: LLMCall;
  diff?: FileDiff;
}

export type EventKind = "delta" | "message" | "interrupt" | "done" | "error" | "trace";

export interface RunEvent {
  event: EventKind;
  text?: string;
  message?: Message;
  interrupts?: Interrupt[];
  trace?: TraceUpdate;
  error?: string;
  namespace?: string[];
}

export interface ThreadState {
  thread_id: string;
  messages: Message[];
  interrupts?: Interrupt[];
  model?: string;
}

export interface ThreadInfo {
  thread_id: string;
  cwd: string;
  title?: string;
  updated_at?: string;
  message_count?: number;
}

// SlashCommand mirrors commands.Command from GET /api/commands.
export interface SlashCommand {
  name: string;
  aliases?: string[];
  description?: string;
  args?: string;
}

export interface Info {
  cwd: string;
  thread_id: string;
  model: string;
  model_override?: string;
  auto_approve: boolean;
  version: string;
}
