import type { FileDiff, Interrupt, LLMCall, Message, Plan, RunEvent, ThreadState, Trace } from "../types";
import { nsKey } from "./messages";

// ThreadView is everything the page knows about the open thread.
export interface ThreadView {
  // messages of the main agent, in thread order
  messages: Message[];
  // messages of subagent runs by namespace key ("task:<call id>")
  sub: Record<string, Message[]>;
  llm: Record<number, LLMCall>;
  diffs: FileDiff[];
  // assistant text streaming in for the main agent
  streamText: string;
  // user text sent but not yet echoed back by the server
  pendingUser: string;
  interrupts: Interrupt[];
  running: boolean;
  error: string;
  // plan is the thread's latest plan as the server last reported it.
  plan: Plan | null;
}

export function emptyView(): ThreadView {
  return {
    messages: [],
    sub: {},
    llm: {},
    diffs: [],
    streamText: "",
    pendingUser: "",
    interrupts: [],
    running: false,
    error: "",
    plan: null,
  };
}

export function fromHistory(ts: ThreadState | null, trace: Trace | null): ThreadView {
  const v = emptyView();
  v.messages = ts?.messages ?? [];
  v.interrupts = ts?.interrupts ?? [];
  v.plan = ts?.plan ?? null;
  if (trace) {
    for (const c of trace.llm_calls) v.llm[c.seq] = c;
    v.diffs = trace.diffs;
    for (const m of trace.messages) {
      const k = nsKey(m.namespace);
      (v.sub[k] ??= []).push(m.message);
    }
  }
  return v;
}

export function startRun(v: ThreadView, userText: string): ThreadView {
  return { ...v, running: true, error: "", pendingUser: userText, interrupts: [], streamText: "" };
}

export function endRun(v: ThreadView, error = ""): ThreadView {
  return { ...v, running: false, streamText: "", pendingUser: "", error };
}

// showBuild reports whether the page should offer to build the plan.
export function showBuild(v: ThreadView): boolean {
  return !v.running && v.interrupts.length === 0 && v.plan?.status === "draft";
}

// messageKey identifies a message across the history page and a replayed
// run stream; messages the server did not name fall back to their content.
function messageKey(m: Message): string {
  if (m.id) return `id:${m.id}`;
  if (m.role === "tool" && m.tool_call_id) return `tool:${m.tool_call_id}`;
  return `content:${m.role}:${JSON.stringify(m.content)}`;
}

function hasMessage(list: Message[], m: Message): boolean {
  const key = messageKey(m);
  return list.some((x) => messageKey(x) === key);
}

function sameDiff(a: FileDiff, b: FileDiff): boolean {
  return a.tool_call_id === b.tool_call_id && a.path === b.path && a.op === b.op && a.time === b.time;
}

// applyEvent folds one run event into the view. A client that reattaches
// to a run replays its events from the start, so messages and diffs the
// view already has are skipped.
export function applyEvent(v: ThreadView, ev: RunEvent): ThreadView {
  switch (ev.event) {
    case "delta":
      if (ev.namespace?.length) return v;
      return { ...v, streamText: v.streamText + (ev.text ?? "") };
    case "message": {
      const m = ev.message;
      if (!m) return v;
      if (ev.namespace?.length) {
        const k = nsKey(ev.namespace);
        const list = v.sub[k] ?? [];
        if (hasMessage(list, m)) return v;
        return { ...v, sub: { ...v.sub, [k]: [...list, m] } };
      }
      return {
        ...v,
        messages: hasMessage(v.messages, m) ? v.messages : [...v.messages, m],
        pendingUser: m.role === "user" ? "" : v.pendingUser,
        streamText: m.role === "assistant" ? "" : v.streamText,
      };
    }
    case "interrupt":
      return { ...v, interrupts: ev.interrupts ?? [] };
    case "trace": {
      const t = ev.trace;
      if (t?.llm_call) return { ...v, llm: { ...v.llm, [t.llm_call.seq]: t.llm_call } };
      if (t?.diff) {
        const d = t.diff;
        return v.diffs.some((x) => sameDiff(x, d)) ? v : { ...v, diffs: [...v.diffs, d] };
      }
      return v;
    }
    case "error":
      return { ...v, error: ev.error ?? "run failed" };
    default:
      return v;
  }
}
