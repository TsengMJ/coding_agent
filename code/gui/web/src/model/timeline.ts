import type { FileDiff, LLMCall, Message, ToolCall } from "../types";
import type { ThreadView } from "./state";
import { reasoningOf, str, textOf, timingOf, traceSeqOf } from "./messages";

// The timeline mirrors the TUI: each user message starts a turn, each model
// reply is an LLM step, and the tool calls it requested hang below it. A
// task call nests the subagent's own steps.

export type ToolStatus = "running" | "ok" | "error" | "stopped" | "awaiting";

export interface ToolStep {
  key: string;
  call: ToolCall;
  result?: Message;
  status: ToolStatus;
  durationMs?: number;
  diffs: FileDiff[];
  sub?: SubRun;
  // the model call that extracted the answer from a fetched page
  extract?: LLMCall;
}

const WEB_EXTRACT = "web_extract:";

export interface BatchSummary {
  count: number;
  wallMs: number;
  sumMs: number;
  parallel: boolean;
}

export interface LLMStep {
  key: string;
  label: string;
  ns: string;
  message: Message;
  call?: LLMCall;
  text: string;
  reasoning: string;
  tools: ToolStep[];
  batch?: BatchSummary;
}

export interface SubRun {
  ns: string;
  number: number;
  type: string;
  description: string;
  steps: LLMStep[];
  report?: string;
  // background runs report through a <task-notification>, not the task result
  background: boolean;
  done: boolean;
}

export interface TaskNotification {
  taskId: string;
  subagentType: string;
  status: string;
  result: string;
}

// parseTaskNotification reads the message the subagents middleware adds when
// a background task finishes.
export function parseTaskNotification(text: string): TaskNotification | null {
  const m =
    /^<task-notification>\n<task-id>(.*)<\/task-id>\n<subagent-type>(.*)<\/subagent-type>\n<status>(.*)<\/status>\n<result>\n([\s\S]*)\n<\/result>\n<\/task-notification>$/.exec(
      text,
    );
  return m ? { taskId: m[1], subagentType: m[2], status: m[3], result: m[4] } : null;
}

export interface Turn {
  key: string;
  index: number;
  user?: Message;
  steps: LLMStep[];
  files: FileDiff[];
  // other messages (e.g. a compaction summary) shown as notes
  notes: Message[];
}

export interface Timeline {
  turns: Turn[];
  tokens: number;
  llmCount: number;
}

export const TASK_TOOL = "task";

interface Ctx {
  view: ThreadView;
  diffsByCall: Map<string, FileDiff[]>;
  extractByCall: Map<string, LLMCall>;
  awaiting: Set<string>;
  subCount: number;
  tokens: number;
}

export function buildTimeline(view: ThreadView): Timeline {
  const ctx: Ctx = {
    view,
    diffsByCall: new Map(),
    extractByCall: new Map(),
    awaiting: new Set(),
    subCount: 0,
    tokens: 0,
  };
  for (const c of Object.values(view.llm)) {
    const ns = c.namespace?.at(-1);
    if (ns?.startsWith(WEB_EXTRACT)) ctx.extractByCall.set(ns.slice(WEB_EXTRACT.length), c);
  }
  for (const d of view.diffs) {
    if (!d.tool_call_id) continue;
    const list = ctx.diffsByCall.get(d.tool_call_id) ?? [];
    list.push(d);
    ctx.diffsByCall.set(d.tool_call_id, list);
  }
  for (const i of view.interrupts) for (const a of i.action_requests) ctx.awaiting.add(a.name);

  const turns: Turn[] = [];
  let turn: Turn | undefined;
  let llmCount = 0;
  const toolIndex = new Map<string, ToolStep>();
  const newTurn = (user?: Message) => {
    turn = { key: `turn-${turns.length}`, index: turns.length, user, steps: [], files: [], notes: [] };
    turns.push(turn);
    return turn;
  };

  for (const m of view.messages) {
    const notice = m.role === "user" ? parseTaskNotification(textOf(m)) : null;
    if (notice) {
      const ts = toolIndex.get(notice.taskId);
      if (ts?.sub) {
        ts.sub.done = true;
        ts.sub.report = notice.result;
        ts.status = notice.status === "completed" ? "ok" : "error";
      }
      continue;
    }
    if (m.role === "user") {
      newTurn(m);
      continue;
    }
    const t = turn ?? newTurn();
    if (m.role === "assistant") {
      llmCount++;
      const step = llmStep(ctx, m, `main · LLM #${llmCount}`, "", `m${llmCount}`);
      for (const ts of step.tools) toolIndex.set(ts.call.id, ts);
      t.steps.push(step);
    } else if (m.role === "tool") {
      const ts = m.tool_call_id ? toolIndex.get(m.tool_call_id) : undefined;
      if (ts) settle(ts, m);
    } else {
      t.notes.push(m);
    }
  }
  const lastStep = turns.at(-1)?.steps.at(-1);
  for (const t of turns) {
    for (const s of t.steps) {
      finishStatuses(ctx, s, s === lastStep);
      s.batch = batchOf(s.tools);
    }
    t.files = filesOf(t.steps);
  }
  return { turns, tokens: ctx.tokens, llmCount };
}

function llmStep(ctx: Ctx, m: Message, label: string, ns: string, key: string): LLMStep {
  if (m.usage) ctx.tokens += m.usage.total_tokens;
  const seq = traceSeqOf(m);
  const step: LLMStep = {
    key,
    label,
    ns,
    message: m,
    call: seq !== undefined ? ctx.view.llm[seq] : undefined,
    text: textOf(m),
    reasoning: reasoningOf(m),
    tools: [],
  };
  for (const call of m.tool_calls ?? []) {
    const ts: ToolStep = {
      key: `${key}-${call.id}`,
      call,
      status: "running",
      diffs: ctx.diffsByCall.get(call.id) ?? [],
      extract: ctx.extractByCall.get(call.id),
    };
    if (call.name === TASK_TOOL) ts.sub = subRun(ctx, call);
    step.tools.push(ts);
  }
  return step;
}

function subRun(ctx: Ctx, call: ToolCall): SubRun {
  ctx.subCount++;
  const number = ctx.subCount;
  const ns = `task:${call.id}`;
  const run: SubRun = {
    ns,
    number,
    type: str(call.args["subagent_type"]) || "subagent",
    description: str(call.args["description"]),
    steps: [],
    background: call.args["run_in_background"] === true,
    done: false,
  };
  const index = new Map<string, ToolStep>();
  let n = 0;
  for (const m of ctx.view.sub[ns] ?? []) {
    if (m.role === "assistant") {
      n++;
      const step = llmStep(ctx, m, `#${number} · LLM ${n}`, ns, `${ns}-${n}`);
      for (const ts of step.tools) index.set(ts.call.id, ts);
      run.steps.push(step);
    } else if (m.role === "tool" && m.tool_call_id) {
      const ts = index.get(m.tool_call_id);
      if (ts) settle(ts, m);
    }
  }
  return run;
}

function settle(ts: ToolStep, m: Message) {
  ts.result = m;
  ts.status = m.status === "error" ? "error" : "ok";
  ts.durationMs = timingOf(m)?.durationMs;
  if (!ts.sub) return;
  if (ts.sub.background && ts.status === "ok") {
    // the report arrives later in a notification
    ts.status = "running";
    return;
  }
  ts.sub.done = true;
  ts.sub.report = textOf(m);
}

// finishStatuses marks calls still without a result: running while the run
// streams, awaiting when the latest step is under review, otherwise
// stopped (cancelled).
function finishStatuses(ctx: Ctx, s: LLMStep, last: boolean) {
  for (const ts of s.tools) {
    if (ts.sub?.background && ts.result && !ts.sub.done) {
      ts.status = ctx.view.running ? "running" : "stopped";
    } else if (!ts.result) {
      if (ctx.view.running) ts.status = "running";
      else if (last && ctx.awaiting.has(ts.call.name)) ts.status = "awaiting";
      else ts.status = "stopped";
    }
    if (ts.sub) for (const sub of ts.sub.steps) finishStatuses(ctx, sub, false);
  }
}

export function batchOf(tools: ToolStep[]): BatchSummary | undefined {
  if (tools.length < 2) return undefined;
  const timings = tools.map((t) => timingOf(t.result));
  if (timings.some((t) => !t)) return undefined;
  const ts = timings as { start: number; durationMs: number }[];
  const start = Math.min(...ts.map((t) => t.start));
  const end = Math.max(...ts.map((t) => t.start + t.durationMs));
  const sum = ts.reduce((a, t) => a + t.durationMs, 0);
  const wall = end - start;
  return { count: ts.length, wallMs: wall, sumMs: sum, parallel: wall < (sum * 4) / 5 };
}

function filesOf(steps: LLMStep[]): FileDiff[] {
  const out: FileDiff[] = [];
  const walk = (ss: LLMStep[]) => {
    for (const s of ss) {
      for (const t of s.tools) {
        out.push(...t.diffs);
        if (t.sub) walk(t.sub.steps);
      }
    }
  };
  walk(steps);
  return out;
}

// FileChange groups the diffs of one path within a turn.
export interface FileChange {
  path: string;
  diffs: FileDiff[];
  added: number;
  removed: number;
  created: boolean;
  deleted: boolean;
}

export function groupFiles(diffs: FileDiff[]): FileChange[] {
  const by = new Map<string, FileChange>();
  for (const d of diffs) {
    let fc = by.get(d.path);
    if (!fc) {
      fc = { path: d.path, diffs: [], added: 0, removed: 0, created: false, deleted: false };
      by.set(d.path, fc);
    }
    fc.diffs.push(d);
    fc.added += d.added;
    fc.removed += d.removed;
    if (d.unified.startsWith("--- /dev/null")) fc.created = fc.diffs.length === 1;
    fc.deleted = d.op === "delete";
  }
  return [...by.values()];
}
