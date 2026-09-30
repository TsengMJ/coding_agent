import type { LLMStep, ToolStep } from "../model/timeline";
import { formatDuration, formatTokens, textOf } from "../model/messages";
import { grepMode, parseExecute, parseGrep, parseNumbered, pathList, todosOf } from "../model/tools";

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
      return "cat-edit";
    case "execute":
      return "cat-exec";
    case "task":
      return "cat-task";
    case "write_todos":
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
      const todos = todosOf(t.call);
      return `${todos.filter((x) => x.status === "completed").length}/${todos.length} done`;
    }
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
