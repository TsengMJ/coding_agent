import type { FileDiff, Interrupt, LLMCall, Message, RunEvent, ThreadState, Trace } from "../types";
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
  };
}

export function fromHistory(ts: ThreadState | null, trace: Trace | null): ThreadView {
  const v = emptyView();
  v.messages = ts?.messages ?? [];
  v.interrupts = ts?.interrupts ?? [];
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

// applyEvent folds one run event into the view.
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
        return { ...v, sub: { ...v.sub, [k]: [...(v.sub[k] ?? []), m] } };
      }
      return {
        ...v,
        messages: [...v.messages, m],
        pendingUser: m.role === "user" ? "" : v.pendingUser,
        streamText: m.role === "assistant" ? "" : v.streamText,
      };
    }
    case "interrupt":
      return { ...v, interrupts: ev.interrupts ?? [] };
    case "trace": {
      const t = ev.trace;
      if (t?.llm_call) return { ...v, llm: { ...v.llm, [t.llm_call.seq]: t.llm_call } };
      if (t?.diff) return { ...v, diffs: [...v.diffs, t.diff] };
      return v;
    }
    case "error":
      return { ...v, error: ev.error ?? "run failed" };
    default:
      return v;
  }
}
