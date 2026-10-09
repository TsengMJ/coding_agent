// Helpers for a team server: workspaces, their Files changed panel and the
// workspace browser shown as screenshots.

import type { FileChange } from "../types";

// splitPatch cuts `git diff` output into one unified diff per file, keyed
// by the new path (the old path for deletions).
export function splitPatch(patch: string): Map<string, string> {
  const files = new Map<string, string>();
  const chunks = patch.split(/^(?=diff --git )/m);
  for (const chunk of chunks) {
    if (!chunk.startsWith("diff --git ")) continue;
    const plus = /^\+\+\+ b\/(.+)$/m.exec(chunk);
    const minus = /^--- a\/(.+)$/m.exec(chunk);
    const header = /^diff --git a\/(.+?) b\/(.+)$/m.exec(chunk);
    const path = plus?.[1] ?? header?.[2] ?? minus?.[1];
    if (path) files.set(path, chunk);
  }
  return files;
}

export function totals(files: FileChange[]): { additions: number; deletions: number } {
  return files.reduce((t, f) => ({ additions: t.additions + f.additions, deletions: t.deletions + f.deletions }), { additions: 0, deletions: 0 });
}

const STATUS_LABELS: Record<string, string> = {
  added: "A",
  modified: "M",
  deleted: "D",
  renamed: "R",
  copied: "C",
  type_changed: "T",
};

export const statusLabel = (status: string): string => STATUS_LABELS[status] ?? "?";

// toPageCoords maps a click on the scaled screenshot to page pixels: the
// screenshot's natural size is the page viewport.
export function toPageCoords(
  click: { x: number; y: number },
  shown: { width: number; height: number },
  natural: { width: number; height: number },
): { x: number; y: number } | null {
  if (shown.width <= 0 || shown.height <= 0 || natural.width <= 0 || natural.height <= 0) return null;
  const x = Math.round((click.x * natural.width) / shown.width);
  const y = Math.round((click.y * natural.height) / shown.height);
  if (x < 0 || y < 0 || x > natural.width || y > natural.height) return null;
  return { x, y };
}

// repoName is "owner/repo" for a GitHub URL, else the URL.
export function repoName(url: string): string {
  const m = /^https:\/\/github\.com\/([^/]+\/[^/]+?)(?:\.git)?\/?$/.exec(url);
  return m ? m[1] : url;
}

// prTitle suggests a pull request title from the thread's first request.
export function prTitle(firstRequest: string): string {
  const line = firstRequest.trim().split("\n")[0] ?? "";
  return line.length > 72 ? line.slice(0, 71) + "…" : line;
}
