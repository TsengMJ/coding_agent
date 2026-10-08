import { useState } from "react";

// JsonTree shows a value as a collapsible tree; long strings keep their
// line breaks.
export function JsonTree({ value, depth = 0, name }: { value: unknown; depth?: number; name?: string }) {
  const [open, setOpen] = useState(depth < 2);
  const key = name !== undefined ? <span className="k">{JSON.stringify(name)}: </span> : null;
  if (value === null || typeof value !== "object") {
    return (
      <div className="json" style={{ paddingLeft: depth ? 16 : 0 }}>
        {key}
        <Scalar v={value} />
      </div>
    );
  }
  const entries = Array.isArray(value) ? value.map((v, i) => [String(i), v] as const) : Object.entries(value);
  const [o, c] = Array.isArray(value) ? ["[", "]"] : ["{", "}"];
  return (
    <div className="json" style={{ paddingLeft: depth ? 16 : 0 }}>
      <span className="toggle" onClick={() => setOpen(!open)}>
        {open ? "▾ " : "▸ "}
      </span>
      {key}
      {o}
      {!open && (
        <span className="toggle" onClick={() => setOpen(true)}>
          {" "}
          {entries.length} {Array.isArray(value) ? "items" : "keys"} {c}
        </span>
      )}
      {open && (
        <>
          {entries.map(([k, v]) => (
            <JsonTree key={k} name={Array.isArray(value) ? undefined : k} value={v} depth={depth + 1} />
          ))}
          <div>{c}</div>
        </>
      )}
    </div>
  );
}

function Scalar({ v }: { v: unknown }) {
  if (typeof v === "string") return <span className="s">{JSON.stringify(v).replaceAll("\\n", "\n")}</span>;
  if (typeof v === "number") return <span className="n">{v}</span>;
  if (typeof v === "boolean" || v === null) return <span className="b">{String(v)}</span>;
  return <span>{String(v)}</span>;
}
