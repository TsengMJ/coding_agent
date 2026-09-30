// Unified diff parsing for the GitHub-style diff view.

export type LineKind = "ctx" | "add" | "del";

export interface DiffLine {
  kind: LineKind;
  text: string;
  oldNo?: number;
  newNo?: number;
}

export interface Hunk {
  header: string;
  oldStart: number;
  newStart: number;
  lines: DiffLine[];
}

export interface ParsedDiff {
  oldPath: string;
  newPath: string;
  hunks: Hunk[];
}

const HUNK = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/;

export function parseUnified(text: string): ParsedDiff {
  const out: ParsedDiff = { oldPath: "", newPath: "", hunks: [] };
  let hunk: Hunk | undefined;
  let oldNo = 0;
  let newNo = 0;
  for (const line of text.split("\n")) {
    if (!hunk && line.startsWith("--- ")) {
      out.oldPath = line.slice(4).trim();
      continue;
    }
    if (!hunk && line.startsWith("+++ ")) {
      out.newPath = line.slice(4).trim();
      continue;
    }
    const m = HUNK.exec(line);
    if (m) {
      oldNo = Number(m[1]);
      newNo = Number(m[3]);
      hunk = { header: line, oldStart: oldNo, newStart: newNo, lines: [] };
      out.hunks.push(hunk);
      continue;
    }
    if (!hunk || line.startsWith("\\")) continue;
    const body = line.slice(1);
    switch (line[0]) {
      case "+":
        hunk.lines.push({ kind: "add", text: body, newNo: newNo++ });
        break;
      case "-":
        hunk.lines.push({ kind: "del", text: body, oldNo: oldNo++ });
        break;
      case " ":
        hunk.lines.push({ kind: "ctx", text: body, oldNo: oldNo++, newNo: newNo++ });
        break;
      default:
        // a trailing empty line after the last hunk
        break;
    }
  }
  return out;
}

// gapBefore is how many unchanged lines lie between the previous hunk and h.
export function gapBefore(prev: Hunk | undefined, h: Hunk): number {
  if (!prev) return h.oldStart - 1;
  const prevOldEnd = prev.oldStart + prev.lines.filter((l) => l.kind !== "add").length;
  return h.oldStart - prevOldEnd;
}

export interface SplitRow {
  left?: DiffLine;
  right?: DiffLine;
}

// splitRows pairs each run of deletions with the following run of
// additions, as the side-by-side view shows them.
export function splitRows(lines: DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (l.kind === "ctx") {
      rows.push({ left: l, right: l });
      i++;
      continue;
    }
    const dels: DiffLine[] = [];
    const adds: DiffLine[] = [];
    while (i < lines.length && lines[i].kind === "del") dels.push(lines[i++]);
    while (i < lines.length && lines[i].kind === "add") adds.push(lines[i++]);
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) rows.push({ left: dels[k], right: adds[k] });
  }
  return rows;
}

const LCS_LIMIT = 4_000_000;

// diffLines is a line diff of a and b (longest common subsequence), used
// to preview edits before they run; very large inputs fall back to
// "everything removed, everything added".
export function diffLines(a: string, b: string): DiffLine[] {
  const x = splitKeep(a);
  const y = splitKeep(b);
  if (x.length * y.length > LCS_LIMIT) {
    return [
      ...x.map((t, i) => ({ kind: "del" as const, text: t, oldNo: i + 1 })),
      ...y.map((t, i) => ({ kind: "add" as const, text: t, newNo: i + 1 })),
    ];
  }
  const n = x.length;
  const m = y.length;
  const dp: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = x[i] === y[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n || j < m) {
    if (i < n && j < m && x[i] === y[j]) {
      out.push({ kind: "ctx", text: x[i], oldNo: i + 1, newNo: j + 1 });
      i++;
      j++;
    } else if (i < n && (j >= m || dp[i + 1][j] >= dp[i][j + 1])) {
      out.push({ kind: "del", text: x[i], oldNo: i + 1 });
      i++;
    } else {
      out.push({ kind: "add", text: y[j], newNo: j + 1 });
      j++;
    }
  }
  return out;
}

function splitKeep(s: string): string[] {
  if (s === "") return [];
  const lines = s.split("\n");
  if (lines.at(-1) === "") lines.pop();
  return lines;
}

export function countKinds(lines: DiffLine[]): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const l of lines) {
    if (l.kind === "add") added++;
    else if (l.kind === "del") removed++;
  }
  return { added, removed };
}
