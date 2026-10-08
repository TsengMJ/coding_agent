// Parsers for the text the built-in tools return, so their results can be
// shown as structure (grouped matches, numbered code, exit codes) instead
// of raw text. Every parser tolerates unexpected text by returning null.

import type { Message, ToolCall } from "../types";
import { str } from "./messages";
import { patchPaths } from "./patch";
import { isMerge, planOf, questionsOf, todosOf } from "./plan";

export interface GrepLine {
  no: number;
  text: string;
  match: boolean;
}

export interface GrepFile {
  path: string;
  count?: number;
  lines: GrepLine[];
  // separators between non-adjacent context groups, by index into lines
  breaks: number[];
}

export function grepMode(call: ToolCall): string {
  return str(call.args["output_mode"]) || "files_with_matches";
}

export function parseGrep(output: string, mode: string): GrepFile[] | null {
  const text = output.trim();
  if (text === "" || text === "No matches found") return [];
  const lines = text.split("\n");
  if (mode === "files_with_matches") return lines.map((path) => ({ path, lines: [], breaks: [] }));
  if (mode === "count") {
    const out: GrepFile[] = [];
    for (const l of lines) {
      const m = /^(.*): (\d+)$/.exec(l);
      if (!m) return null;
      out.push({ path: m[1], count: Number(m[2]), lines: [], breaks: [] });
    }
    return out;
  }
  const out: GrepFile[] = [];
  let cur: GrepFile | undefined;
  for (const l of lines) {
    if (!l.startsWith("  ") && l.endsWith(":")) {
      cur = { path: l.slice(0, -1), lines: [], breaks: [] };
      out.push(cur);
      continue;
    }
    if (!cur) return null;
    if (l.trim() === "--") {
      cur.breaks.push(cur.lines.length);
      continue;
    }
    const m = /^ {2}(\d+)([:-]) ?(.*)$/.exec(l);
    if (!m) return null;
    cur.lines.push({ no: Number(m[1]), match: m[2] === ":", text: m[3] });
  }
  for (const f of out) f.count = f.lines.filter((l) => l.match).length;
  return out;
}

export interface CodeLine {
  no: string;
  text: string;
}

// parseNumbered reads read_file output: a right-aligned line number, two
// spaces, then the line.
export function parseNumbered(output: string): CodeLine[] | null {
  if (output === "") return [];
  const out: CodeLine[] = [];
  for (const l of output.split("\n")) {
    const m = /^\s*(\d+(?:\.\d+)?) {2}(.*)$/.exec(l) ?? /^\s*(\d+(?:\.\d+)?)$/.exec(l);
    if (!m) return null;
    out.push({ no: m[1], text: m[2] ?? "" });
  }
  return out;
}

export interface ExecResult {
  output: string;
  exitCode?: number;
  truncated: boolean;
}

export function parseExecute(output: string): ExecResult {
  let rest = output;
  let truncated = false;
  const trunc = "\n[Output was truncated due to size limits]";
  if (rest.endsWith(trunc)) {
    truncated = true;
    rest = rest.slice(0, -trunc.length);
  }
  const m = /\n?\[Command (?:succeeded|failed) with exit code (-?\d+)\]$/.exec(rest);
  if (m) return { output: rest.slice(0, m.index), exitCode: Number(m[1]), truncated };
  return { output: rest, truncated };
}

export interface SandboxInfo {
  // mode is the sandbox mode (e.g. "workspace-write"), or "unsandboxed"
  // for an approved escape.
  mode: string;
  blocked: boolean;
}

export const UNSANDBOXED = "unsandboxed";

// sandboxOf reads the sandbox tag of a shell tool result: metadata.sandbox,
// or for older threads the execute artifact's fields.
export function sandboxOf(result?: Message): SandboxInfo | null {
  const md = result?.metadata;
  if (!md) return null;
  if (typeof md["sandbox"] === "string" && md["sandbox"]) {
    return { mode: md["sandbox"], blocked: md["sandbox_blocked"] === true };
  }
  const art = md["artifact"] as Record<string, unknown> | undefined;
  if (art && typeof art["sandbox"] === "string" && art["sandbox"]) {
    return { mode: art["sandbox"], blocked: art["sandbox_blocked"] === true };
  }
  return null;
}

// sandboxNote is the short marker shown in a collapsed tool row; ordinary
// sandboxed runs get none.
export function sandboxNote(s: SandboxInfo | null): string {
  if (!s) return "";
  if (s.mode === UNSANDBOXED) return UNSANDBOXED;
  return s.blocked ? "sandbox blocked" : "";
}

export interface TreeNode {
  name: string;
  path: string;
  dir: boolean;
  children: TreeNode[];
}

// buildTree turns a list of paths (ls, glob, grep files) into a directory
// tree; paths ending in "/" are directories.
export function buildTree(paths: string[]): TreeNode {
  const root: TreeNode = { name: "/", path: "/", dir: true, children: [] };
  for (const raw of paths) {
    const isDir = raw.endsWith("/");
    const parts = raw.split("/").filter(Boolean);
    let node = root;
    parts.forEach((part, i) => {
      const last = i === parts.length - 1;
      let child = node.children.find((c) => c.name === part);
      if (!child) {
        child = { name: part, path: "/" + parts.slice(0, i + 1).join("/"), dir: !last || isDir, children: [] };
        node.children.push(child);
      } else if (!last) {
        child.dir = true;
      }
      node = child;
    });
  }
  const sort = (n: TreeNode) => {
    n.children.sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name));
    n.children.forEach(sort);
  };
  sort(root);
  return root;
}

export function pathList(output: string): string[] {
  const t = output.trim();
  if (t === "" || t === "No files found") return [];
  return t.split("\n").filter((l) => l !== "");
}

// summarize is the one-line label of a tool call in the collapsed tree.
export function summarize(call: ToolCall): string {
  const a = call.args;
  const s = (k: string) => str(a[k]);
  const first = (v: string) => {
    const i = v.indexOf("\n");
    return i >= 0 ? v.slice(0, i) + " …" : v;
  };
  switch (call.name) {
    case "read_file": {
      const off = Number(a["offset"] ?? 0);
      const lim = a["limit"] !== undefined ? Number(a["limit"]) : undefined;
      const range = off || lim ? ` [${off + 1}–${lim ? off + lim : "end"}]` : "";
      return `${s("file_path")}${range}`;
    }
    case "grep": {
      const parts = [JSON.stringify(s("pattern"))];
      if (s("path")) parts.push(`in ${s("path")}`);
      if (s("glob")) parts.push(`(${s("glob")})`);
      return parts.join(" ");
    }
    case "glob":
      return s("path") ? `${s("pattern")} in ${s("path")}` : s("pattern");
    case "ls":
      return s("path");
    case "write_file":
    case "edit_file":
    case "delete":
      return s("file_path");
    case "apply_patch": {
      const paths = patchPaths(s("patch"));
      return paths.length > 3 ? `${paths.slice(0, 3).join(", ")} +${paths.length - 3} more` : paths.join(", ");
    }
    case "execute":
      return first(s("command"));
    case "write_todos": {
      const n = todosOf(call).length;
      return isMerge(call) ? `update ${n} item${n === 1 ? "" : "s"}` : `${n} items`;
    }
    case "create_plan": {
      const p = planOf(call);
      return `${p.name} · ${p.todos.length} todos`;
    }
    case "ask_user": {
      const n = questionsOf(call.args)?.length ?? 0;
      return `${n} question${n === 1 ? "" : "s"}`;
    }
    case "task":
      return `${s("subagent_type")} · ${first(s("description"))}${a["run_in_background"] === true ? " · background" : ""}`;
    case "task_output":
      return s("task_id");
    case "web_search":
      return s("query");
    case "fetch_url":
      return s("prompt") ? `${s("url")} · ${first(s("prompt"))}` : s("url");
    case "resolve_library_id":
      return `${s("library_name")} · ${first(s("query"))}`;
    case "query_docs":
      return `${s("library_id")} · ${first(s("query"))}`;
    default: {
      const kv = Object.entries(a)
        .map(([k, v]) => `${k}=${first(str(v))}`)
        .join(" ");
      return kv.length > 120 ? kv.slice(0, 120) + "…" : kv;
    }
  }
}

export interface Library {
  title: string;
  id: string;
  description: string;
  stats: string;
  versions: string;
}

// parseLibraries reads the resolve_library_id result: a numbered entry per
// library with indented description, stats and versions lines.
export function parseLibraries(output: string): Library[] | null {
  const lines = output.split("\n");
  if (!lines[0]?.startsWith("Context7 libraries for ")) return null;
  const out: Library[] = [];
  for (const l of lines.slice(1)) {
    const head = /^\d+\. (.*) — (\/\S+)$/.exec(l);
    if (head) {
      out.push({ title: head[1], id: head[2], description: "", stats: "", versions: "" });
      continue;
    }
    const cur = out.at(-1);
    const body = l.trim();
    if (!cur || body === "") continue;
    if (body.startsWith("Snippets:")) cur.stats = body;
    else if (body.startsWith("Versions:")) cur.versions = body.slice("Versions: ".length).replace(/ \(pin one as .*\)$/, "");
    else cur.description = body;
  }
  return out;
}

// commandLine renders a call as the equivalent shell-style command, so a
// reader sees exactly what ran.
export function commandLine(call: ToolCall): string {
  const a = call.args;
  const s = (k: string) => str(a[k]);
  const q = (v: string) => (/^[\w./-]+$/.test(v) ? v : `'${v.replaceAll("'", "'\\''")}'`);
  switch (call.name) {
    case "execute":
      return s("command");
    case "grep": {
      const flags = ["-F"];
      const mode = grepMode(call);
      if (mode === "files_with_matches") flags.push("-l");
      if (mode === "count") flags.push("-c");
      if (mode === "content") flags.push("-n");
      if (a["max_count"] !== undefined) flags.push(`-m ${s("max_count")}`);
      if (s("glob")) flags.push(`--include=${q(s("glob"))}`);
      return `grep -r ${flags.join(" ")} ${q(s("pattern"))} ${q(s("path") || "/")}`;
    }
    case "glob":
      return `glob ${q(s("pattern"))}${s("path") ? " " + q(s("path")) : ""}`;
    case "ls":
      return `ls ${q(s("path"))}`;
    case "read_file": {
      const off = Number(a["offset"] ?? 0);
      const lim = Number(a["limit"] ?? 2000);
      return `sed -n '${off + 1},${off + lim}p' ${q(s("file_path"))}`;
    }
    default:
      return `${call.name} ${JSON.stringify(a)}`;
  }
}

const LANGS: Record<string, string> = {
  go: "go",
  ts: "typescript",
  tsx: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  py: "python",
  rs: "rust",
  java: "java",
  kt: "kotlin",
  rb: "ruby",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "ini",
  md: "markdown",
  css: "css",
  scss: "scss",
  html: "xml",
  xml: "xml",
  sql: "sql",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  hpp: "cpp",
  swift: "swift",
  php: "php",
  mod: "go",
};

export function languageOf(path: string): string | undefined {
  const base = path.split("/").pop() ?? "";
  if (base === "Makefile") return "makefile";
  if (base === "Dockerfile") return "dockerfile";
  const ext = base.includes(".") ? base.split(".").pop()!.toLowerCase() : "";
  return LANGS[ext];
}
