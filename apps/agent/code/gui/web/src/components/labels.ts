import type { LLMStep, ToolStep } from "../model/timeline";
import { formatDuration, formatTokens, textOf } from "../model/messages";
import { grepMode, parseExecute, parseGrep, parseNumbered, pathList, sandboxNote, sandboxOf } from "../model/tools";
import { currentTodos, planOf } from "../model/plan";

export function category(name: string): string {
  switch (name) {
    case "read_file":
    case "ls":
      return "cat-read";
    case "grep":
    case "glob":
      return "cat-search";
    case "write_file":
    case "edit_file":
    case "delete":
    case "apply_patch":
      return "cat-edit";
    case "execute":
    case "run_tests":
    case "diagnostics":
      return "cat-exec";
    case "git_status":
    case "git_diff":
      return "cat-read";
    case "task":
      return "cat-task";
    case "write_todos":
    case "create_plan":
    case "ask_user":
      return "cat-plan";
    default:
      return "cat-other";
  }
}

// resultSummary is the short outcome shown at the right of a tool row.
export function resultSummary(t: ToolStep): string {
  if (!t.result) return "";
  const out = textOf(t.result);
  if (t.status === "error") return firstLine(out);
  return [outcome(t, out), sandboxNote(sandboxOf(t.result))].filter(Boolean).join(" · ");
}

function outcome(t: ToolStep, out: string): string {
  switch (t.call.name) {
    case "grep": {
      const mode = grepMode(t.call);
      const files = parseGrep(out, mode);
      if (!files) return "";
      if (files.length === 0) return "no matches";
      if (mode === "files_with_matches") return `${files.length} files`;
      const n = files.reduce((a, f) => a + (f.count ?? 0), 0);
      return `${n} matches · ${files.length} files`;
    }
    case "read_file": {
      const lines = parseNumbered(out);
      return lines ? `${lines.length} lines` : "";
    }
    case "ls":
    case "glob":
      return `${pathList(out).length} entries`;
    case "execute": {
      const r = parseExecute(out);
      return r.exitCode !== undefined ? `exit ${r.exitCode}` : "";
    }
    case "write_todos": {
      const todos = currentTodos(t.call, out);
      return `${todos.filter((x) => x.status === "completed").length}/${todos.length} done`;
    }
    case "create_plan":
      return `plan saved · ${planOf(t.call).todos.length} todos`;
    case "ask_user":
      return "answered";
    default:
      return "";
  }
}

export function llmMeta(s: LLMStep): string {
  const parts: string[] = [];
  const u = s.message.usage ?? s.call?.usage;
  if (u) {
    let p = `${formatTokens(u.input_tokens)} in / ${formatTokens(u.output_tokens)} out`;
    if (u.cache_read) p += ` (${formatTokens(u.cache_read)} cached)`;
    parts.push(p);
  }
  const md = s.message.metadata;
  const dur = md?.["duration_ms"] ?? s.call?.duration_ms;
  if (typeof dur === "number") parts.push(formatDuration(dur));
  return parts.join(" · ");
}

export function firstLine(s: string): string {
  const l = s.split("\n")[0] ?? "";
  return l.length > 100 ? l.slice(0, 100) + "…" : l;
}
