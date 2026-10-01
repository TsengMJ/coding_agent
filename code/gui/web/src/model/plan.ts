// Readers for write_todos, create_plan and ask_user, mirroring
// middleware/todolist, middleware/planmode and code/askuser.

import type { Answer, Question, Todo, ToolCall } from "../types";
import { str } from "./messages";

export const TODOS_TOOL = "write_todos";
export const PLAN_TOOL = "create_plan";
export const ASK_TOOL = "ask_user";
// BUILD_MESSAGE matches client.BuildMessage.
export const BUILD_MESSAGE = "Build the approved plan.";
const RESULT_PREFIX = "Updated todo list to ";

function toTodo(raw: unknown, i: number): Todo {
  const t = (raw ?? {}) as Record<string, unknown>;
  const notes = str(t["notes"]);
  return {
    id: str(t["id"]) || String(i + 1),
    content: str(t["content"]),
    status: str(t["status"]) || "pending",
    ...(notes ? { notes } : {}),
  };
}

// todosOf reads the todos in a write_todos call; with merge=true these are
// only the items that changed.
export function todosOf(call: ToolCall): Todo[] {
  const list = call.args["todos"];
  return Array.isArray(list) ? list.map(toTodo) : [];
}

export function isMerge(call: ToolCall): boolean {
  return call.args["merge"] === true;
}

// todosFromResult reads the full list back from a write_todos result, or
// null when the text is not a successful result.
export function todosFromResult(text: string): Todo[] | null {
  if (!text.startsWith(RESULT_PREFIX)) return null;
  try {
    const list = JSON.parse(text.slice(RESULT_PREFIX.length)) as unknown;
    return Array.isArray(list) ? list.map(toTodo) : null;
  } catch {
    return null;
  }
}

// currentTodos is the full list after a write_todos call: its result when
// present, else the call's own items.
export function currentTodos(call: ToolCall, resultText?: string): Todo[] {
  return (resultText ? todosFromResult(resultText) : null) ?? todosOf(call);
}

export interface PlanDraft {
  name: string;
  overview: string;
  body: string;
  todos: Todo[];
}

// planOf reads a create_plan call.
export function planOf(call: ToolCall): PlanDraft {
  const list = call.args["todos"];
  return {
    name: str(call.args["name"]),
    overview: str(call.args["overview"]),
    body: str(call.args["plan"]),
    todos: Array.isArray(list) ? list.map((t, i) => ({ ...toTodo(t, i), status: "pending" })) : [],
  };
}

// planID reads the saved plan id from a create_plan result.
export function planID(resultText: string): string {
  const m = /^Plan (\S+) saved with \d+ todos/.exec(resultText);
  return m ? m[1] : "";
}

// questionsOf reads ask_user questions, or null when they are malformed.
export function questionsOf(args: Record<string, unknown>): Question[] | null {
  const list = args["questions"];
  if (!Array.isArray(list) || list.length === 0) return null;
  const out: Question[] = [];
  for (const raw of list) {
    const q = (raw ?? {}) as Record<string, unknown>;
    const opts = q["options"];
    if (!str(q["id"]) || !str(q["prompt"]) || !Array.isArray(opts) || opts.length < 2) return null;
    out.push({
      id: str(q["id"]),
      prompt: str(q["prompt"]),
      allow_multiple: q["allow_multiple"] === true,
      options: opts.map((o) => {
        const r = (o ?? {}) as Record<string, unknown>;
        return { id: str(r["id"]), label: str(r["label"]) };
      }),
    });
  }
  return out;
}

// formatAnswers renders the answers like askuser.FormatAnswers.
export function formatAnswers(qs: Question[], answers: Answer[]): string {
  const byId = new Map(answers.map((a) => [a.question_id, a]));
  const lines = ["The user answered:"];
  qs.forEach((q, i) => {
    const a = byId.get(q.id);
    const picked = q.options.filter((o) => a?.option_ids.includes(o.id)).map((o) => o.label);
    const other = a?.other?.trim();
    if (other) picked.push(`Other: ${other}`);
    lines.push(`${i + 1}. ${q.prompt}`, `   -> ${picked.length ? picked.join("; ") : "(no answer)"}`);
  });
  return lines.join("\n");
}

export function todoMark(status: string): string {
  switch (status) {
    case "completed":
      return "☑";
    case "in_progress":
      return "◐";
    case "cancelled":
      return "⊘";
    default:
      return "☐";
  }
}
