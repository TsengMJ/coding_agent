import { useMemo, useState } from "react";
import { gapBefore, parseUnified, splitRows, type DiffLine, type Hunk } from "../model/diff";
import { languageOf } from "../model/tools";
import { highlight } from "../highlight";
import { CopyButton, DiffStat } from "./common";

type Mode = "unified" | "split";

let preferred: Mode = "unified";

// DiffView renders a unified diff the way GitHub does: hunk headers,
// old/new line numbers, red deletions, green additions, and a toggle to a
// side-by-side view.
export function DiffView({
  unified,
  hunks: given,
  path,
  added,
  removed,
  label,
}: {
  unified?: string;
  hunks?: Hunk[];
  path: string;
  added: number;
  removed: number;
  label?: string;
}) {
  const [mode, setMode] = useState<Mode>(preferred);
  const hunks = useMemo(() => given ?? parseUnified(unified ?? "").hunks, [given, unified]);
  const lang = languageOf(path);
  const choose = (m: Mode) => {
    preferred = m;
    setMode(m);
  };
  return (
    <div>
      <div className="file-head">
        <span className="grow">{path}</span>
        {label && <span className="badge">{label}</span>}
        <DiffStat added={added} removed={removed} />
        <span className="seg">
          <button className={mode === "unified" ? "active" : ""} onClick={() => choose("unified")}>
            Unified
          </button>
          <button className={mode === "split" ? "active" : ""} onClick={() => choose("split")}>
            Split
          </button>
        </span>
        {unified && <CopyButton text={unified} label="Copy patch" />}
      </div>
      <div className="diff">
        {hunks.length === 0 ? (
          <div className="muted" style={{ padding: 8 }}>
            No textual changes.
          </div>
        ) : mode === "unified" ? (
          <UnifiedTable hunks={hunks} lang={lang} />
        ) : (
          <SplitTable hunks={hunks} lang={lang} />
        )}
      </div>
    </div>
  );
}

function Gap({ prev, h, cols }: { prev?: Hunk; h: Hunk; cols: number }) {
  const n = gapBefore(prev, h);
  if (n <= 0) return null;
  return (
    <tr className="gap">
      <td colSpan={cols}>⋯ {n} unchanged line{n === 1 ? "" : "s"}</td>
    </tr>
  );
}

function Src({ line, lang, className }: { line?: DiffLine; lang?: string; className?: string }) {
  return <td className={className} dangerouslySetInnerHTML={{ __html: line ? highlight(line.text, lang) || " " : "" }} />;
}

function UnifiedTable({ hunks, lang }: { hunks: Hunk[]; lang?: string }) {
  return (
    <table>
      <colgroup>
        <col style={{ width: 48 }} />
        <col style={{ width: 48 }} />
        <col style={{ width: 18 }} />
        <col />
      </colgroup>
      <tbody>
        {hunks.map((h, hi) => (
          <HunkRows key={hi} prev={hunks[hi - 1]} h={h} lang={lang} />
        ))}
      </tbody>
    </table>
  );
}

function HunkRows({ prev, h, lang }: { prev?: Hunk; h: Hunk; lang?: string }) {
  return (
    <>
      <Gap prev={prev} h={h} cols={4} />
      <tr className="hunk">
        <td colSpan={4}>{h.header}</td>
      </tr>
      {h.lines.map((l, i) => (
        <tr key={i} className={l.kind === "ctx" ? "" : l.kind}>
          <td className="ln">{l.oldNo ?? ""}</td>
          <td className="ln">{l.newNo ?? ""}</td>
          <td className="sign">{l.kind === "add" ? "+" : l.kind === "del" ? "−" : " "}</td>
          <Src line={l} lang={lang} />
        </tr>
      ))}
    </>
  );
}

function SplitTable({ hunks, lang }: { hunks: Hunk[]; lang?: string }) {
  return (
    <table>
      <colgroup>
        <col style={{ width: 48 }} />
        <col />
        <col style={{ width: 48 }} />
        <col />
      </colgroup>
      <tbody>
        {hunks.map((h, hi) => (
          <SplitHunk key={hi} prev={hunks[hi - 1]} h={h} lang={lang} />
        ))}
      </tbody>
    </table>
  );
}

function SplitHunk({ prev, h, lang }: { prev?: Hunk; h: Hunk; lang?: string }) {
  const rows = splitRows(h.lines);
  const cls = (l?: DiffLine) => (!l ? "empty" : l.kind === "ctx" ? "" : l.kind);
  return (
    <>
      <Gap prev={prev} h={h} cols={4} />
      <tr className="hunk">
        <td colSpan={4}>{h.header}</td>
      </tr>
      {rows.map((r, i) => (
        <tr key={i}>
          <td className={`ln ${cls(r.left)}`}>{r.left?.oldNo ?? ""}</td>
          <Src line={r.left} lang={lang} className={cls(r.left)} />
          <td className={`ln ${cls(r.right)}`}>{r.right?.newNo ?? ""}</td>
          <Src line={r.right} lang={lang} className={cls(r.right)} />
        </tr>
      ))}
    </>
  );
}
