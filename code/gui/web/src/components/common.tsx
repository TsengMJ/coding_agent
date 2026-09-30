import { useMemo, useState, type ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { highlight } from "../highlight";

export function Markdown({ text }: { text: string }) {
  return (
    <div className="md">
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  );
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      className="copy"
      title="Copy to clipboard"
      onClick={(e) => {
        e.stopPropagation();
        void navigator.clipboard.writeText(text).then(() => {
          setDone(true);
          setTimeout(() => setDone(false), 1200);
        });
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}

export function Command({ text, prompt = "$" }: { text: string; prompt?: string }) {
  return (
    <div className="cmd">
      <span className="prompt">{prompt}</span>
      <pre>{text}</pre>
      <CopyButton text={text} />
    </div>
  );
}

export function Section({ title, children, extra }: { title: string; children: ReactNode; extra?: ReactNode }) {
  return (
    <>
      <div className="section-title">
        {title} {extra}
      </div>
      {children}
    </>
  );
}

// Highlighted renders text with occurrences of query marked.
export function Highlighted({ text, query }: { text: string; query: string }) {
  if (!query) return <>{text}</>;
  const parts: ReactNode[] = [];
  const lower = text.toLowerCase();
  const q = query.toLowerCase();
  let i = 0;
  let k = 0;
  for (;;) {
    const j = lower.indexOf(q, i);
    if (j < 0) break;
    parts.push(text.slice(i, j), <mark key={k++}>{text.slice(j, j + q.length)}</mark>);
    i = j + q.length;
  }
  parts.push(text.slice(i));
  return <>{parts}</>;
}

export interface CodeRow {
  no?: string | number;
  text: string;
  hit?: boolean;
  sep?: boolean;
}

// Code shows numbered, syntax-highlighted lines; mark highlights a literal
// string (e.g. the grep pattern).
export function Code({ rows, lang, mark }: { rows: CodeRow[]; lang?: string; mark?: string }) {
  const html = useMemo(() => rows.map((r) => (r.sep ? "" : highlight(r.text, lang, mark))), [rows, lang, mark]);
  return (
    <div className="code">
      <table>
        <tbody>
          {rows.map((r, i) =>
            r.sep ? (
              <tr key={i} className="sep">
                <td colSpan={2}>⋯</td>
              </tr>
            ) : (
              <tr key={i} className={r.hit ? "hit" : undefined}>
                <td className="ln">{r.no ?? i + 1}</td>
                <td dangerouslySetInnerHTML={{ __html: html[i] || " " }} />
              </tr>
            ),
          )}
        </tbody>
      </table>
    </div>
  );
}

export function Tabs<T extends string>({
  tabs,
  active,
  onChange,
  extra,
}: {
  tabs: { id: T; label: string }[];
  active: T;
  onChange: (t: T) => void;
  extra?: ReactNode;
}) {
  return (
    <div className="detail-tabs">
      {tabs.map((t) => (
        <button key={t.id} className={t.id === active ? "active" : ""} onClick={() => onChange(t.id)}>
          {t.label}
        </button>
      ))}
      <span className="grow" />
      {extra}
    </div>
  );
}

export function DiffStat({ added, removed }: { added: number; removed: number }) {
  const total = added + removed;
  const boxes = 5;
  const a = total === 0 ? 0 : Math.round((added / total) * boxes);
  const d = total === 0 ? 0 : Math.min(boxes - a, Math.round((removed / total) * boxes));
  return (
    <span className="mono" style={{ display: "inline-flex", gap: 6, alignItems: "center" }}>
      <span className="plus">+{added}</span>
      <span className="minus">−{removed}</span>
      <span className="diffstat">
        {Array.from({ length: boxes }, (_, i) => (
          <i key={i} className={i < a ? "a" : i < a + d ? "d" : ""} />
        ))}
      </span>
    </span>
  );
}
