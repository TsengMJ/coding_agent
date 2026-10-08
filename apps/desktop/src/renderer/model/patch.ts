// Parsing of apply_patch's Codex-style patches for previews. Hunks carry no
// line numbers, so previews number lines from 1 within each hunk.

import type { DiffLine, Hunk } from "./diff";

export interface PatchFile {
  op: "add" | "delete" | "update";
  path: string;
  moveTo?: string;
  hunks: Hunk[];
}

const HEADERS: [PatchFile["op"], string][] = [
  ["add", "*** Add File:"],
  ["delete", "*** Delete File:"],
  ["update", "*** Update File:"],
];

// parsePatch returns the files of a patch, or null when it is not a patch.
export function parsePatch(text: string): PatchFile[] | null {
  const files: PatchFile[] = [];
  let file: PatchFile | undefined;
  let hunk: Hunk | undefined;
  let oldNo = 1;
  let newNo = 1;
  const startHunk = (header: string) => {
    hunk = { header, oldStart: 1, newStart: 1, lines: [] };
    oldNo = 1;
    newNo = 1;
    file!.hunks.push(hunk);
  };
  for (const raw of text.replaceAll("\r\n", "\n").split("\n")) {
    const line = raw.trimEnd();
    const header = HEADERS.find(([, prefix]) => line.startsWith(prefix));
    if (header) {
      file = { op: header[0], path: line.slice(header[1].length).trim(), hunks: [] };
      files.push(file);
      hunk = undefined;
      if (file.op === "add") startHunk("@@ new file @@");
      continue;
    }
    if (!file) continue;
    if (line.startsWith("*** Move to:")) {
      file.moveTo = line.slice("*** Move to:".length).trim();
      continue;
    }
    if (line.startsWith("***")) continue;
    if (line.startsWith("@@")) {
      startHunk(line === "@@" ? "@@" : `${line} @@`);
      continue;
    }
    if (file.op === "delete") continue;
    if (!hunk) startHunk("@@");
    const body = raw.slice(1);
    let l: DiffLine | undefined;
    switch (raw[0]) {
      case "+":
        l = { kind: "add", text: body, newNo: newNo++ };
        break;
      case "-":
        l = { kind: "del", text: body, oldNo: oldNo++ };
        break;
      case " ":
        l = { kind: "ctx", text: body, oldNo: oldNo++, newNo: newNo++ };
        break;
      default:
        if (raw === "") l = { kind: "ctx", text: "", oldNo: oldNo++, newNo: newNo++ };
    }
    if (l) hunk!.lines.push(l);
  }
  return files.length > 0 ? files : null;
}

// patchPaths lists the paths a patch touches, for one-line summaries.
export function patchPaths(text: string): string[] {
  return (parsePatch(text) ?? []).map((f) => (f.moveTo ? `${f.path} → ${f.moveTo}` : f.path));
}
