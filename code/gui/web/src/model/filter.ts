import type { LLMStep, ToolStep, Turn } from "./timeline";
import { textOf } from "./messages";
import { parseExecute, summarize } from "./tools";

export type Filter = "all" | "edits" | "commands" | "errors" | "llm";

// failedCommand reports an execute call that ran but exited non-zero; the
// tool itself succeeded, so its status alone would look green.
export function failedCommand(t: ToolStep): boolean {
  if (t.call.name !== "execute" || !t.result || t.status === "error") return false;
  const code = parseExecute(textOf(t.result)).exitCode;
  return code !== undefined && code !== 0;
}

function eachTool(steps: LLMStep[], fn: (t: ToolStep) => boolean): boolean {
  for (const s of steps) {
    for (const t of s.tools) {
      if (fn(t)) return true;
      if (t.sub && eachTool(t.sub.steps, fn)) return true;
    }
  }
  return false;
}

// matchTurn reports whether a turn should stay visible under filter and a
// case-insensitive search query.
export function matchTurn(turn: Turn, filter: Filter, query: string): boolean {
  const byFilter = (() => {
    switch (filter) {
      case "all":
      case "llm":
        return true;
      case "edits":
        return turn.files.length > 0 || eachTool(turn.steps, (t) => ["write_file", "edit_file", "delete", "apply_patch"].includes(t.call.name));
      case "commands":
        return eachTool(turn.steps, (t) => ["execute", "run_tests", "diagnostics"].includes(t.call.name));
      case "errors":
        return eachTool(turn.steps, (t) => t.status === "error" || failedCommand(t)) || turn.steps.some((s) => !!s.call?.error);
    }
  })();
  if (!byFilter) return false;
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const has = (s: string) => s.toLowerCase().includes(q);
  if (turn.user && has(textOf(turn.user))) return true;
  const inSteps = (steps: LLMStep[]): boolean =>
    steps.some(
      (s) =>
        has(s.text) ||
        s.tools.some(
          (t) =>
            has(summarize(t.call)) ||
            has(JSON.stringify(t.call.args)) ||
            has(textOf(t.result)) ||
            (t.sub ? inSteps(t.sub.steps) : false),
        ),
    );
  return inSteps(turn.steps);
}