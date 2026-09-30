// Parsers for the text the built-in tools return, so their results can be
// shown as structure (grouped matches, numbered code, exit codes) instead
// of raw text. Every parser tolerates unexpected text by returning null.

import type { ToolCall } from "../types";
import { str } from "./messages";

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

export interface Todo {
  content: string;
  status: string;
}

export function todosOf(call: ToolCall): Todo[] {
  const list = call.args["todos"];
  if (!Array.isArray(list)) return [];
  return list.map((t) => ({ content: str((t as Todo).content), status: str((t as Todo).status) }));
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
    case "execute":
      return first(s("command"));
    case "write_todos":
      return `${todosOf(call).length} items`;
    case "task":
      return `${s("subagent_type")} · ${first(s("description"))}`;
    default: {
      const kv = Object.entries(a)
        .map(([k, v]) => `${k}=${first(str(v))}`)
        .join(" ");
      return kv.length > 120 ? kv.slice(0, 120) + "…" : kv;
    }
  }
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
