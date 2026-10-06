// Mirrors of the Go JSON types served by code/server and code/trace.

export type Role = "system" | "user" | "assistant" | "tool";

export interface ContentBlock {
  type: string;
  text?: string;
  mime_type?: string;
  base64?: string;
  url?: string;
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

export type TodoStatus = "pending" | "in_progress" | "completed" | "cancelled";

// Todo mirrors todolist.Todo.
export interface Todo {
  id: string;
  content: string;
  status: TodoStatus | string;
  notes?: string;
}

// Plan mirrors planmode.Plan.
export interface Plan {
  name: string;
  overview: string;
  body: string;
  todos: Todo[];
  id: string;
  status: "draft" | "building" | string;
}

export type RunMode = "agent" | "plan";

export interface ThreadState {
  thread_id: string;
  messages: Message[];
  interrupts?: Interrupt[];
  model?: string;
  todos?: Todo[];
  plan?: Plan;
}

export interface HistoryPage {
  thread_id: string;
  checkpoint_seq: number;
  messages: Message[];
  interrupts?: Interrupt[];
  model?: string;
  todos?: Todo[];
  plan?: Plan;
  has_more: boolean;
  next_before?: number;
}

// Question, Option and Answer mirror code/askuser.
export interface QuestionOption {
  id: string;
  label: string;
}

export interface Question {
  id: string;
  prompt: string;
  options: QuestionOption[];
  allow_multiple?: boolean;
}

export interface Answer {
  question_id: string;
  option_ids: string[];
  other?: string;
}

// ThreadUsage mirrors server.ThreadUsage from GET /api/threads/{id}/usage.
export interface ModelUsage {
  model: string;
  calls: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens?: number;
  cache_write_tokens?: number;
  cost_usd?: number;
}

export interface ThreadUsage {
  thread_id: string;
  model: string;
  context_tokens: number;
  context_limit?: number;
  context_percent?: number;
  conversation_tokens: number;
  fixed_tokens: number;
  models: ModelUsage[];
  total_cost_usd: number;
  unpriced?: string[];
}

export interface ThreadInfo {
  thread_id: string;
  cwd: string;
  title?: string;
  updated_at?: string;
  message_count?: number;
  running?: boolean;
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
