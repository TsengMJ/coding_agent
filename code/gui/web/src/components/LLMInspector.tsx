import { useEffect, useState } from "react";
import type { Api } from "../api";
import type { LLMCallDetail, Message } from "../types";
import { formatDuration, formatTokens, reasoningOf, textOf } from "../model/messages";
import { CopyButton, Markdown, Tabs } from "./common";
import { JsonTree } from "./JsonTree";

type Tab = "overview" | "input" | "output" | "tools" | "raw";

const ROLE_COLORS: Record<string, string> = {
  system: "var(--muted)",
  user: "var(--accent)",
  assistant: "var(--purple)",
  tool: "var(--green)",
};

// LLMInspector shows one model call in full: what was sent (system prompt,
// every message, the tool list) and what came back.
export function LLMInspector({
  api,
  threadId,
  seq,
  label,
  onClose,
}: {
  api: Api;
  threadId: string;
  seq: number;
  label: string;
  onClose: () => void;
}) {
  const [detail, setDetail] = useState<LLMCallDetail | null>(null);
  const [error, setError] = useState("");
  const [tab, setTab] = useState<Tab>("overview");
  useEffect(() => {
    let live = true;
    setDetail(null);
    setError("");
    api
      .llmCall(threadId, seq)
      .then((d) => live && setDetail(d))
      .catch((e: Error) => live && setError(e.message));
    return () => {
      live = false;
    };
  }, [api, threadId, seq]);

  return (
    <aside className="drawer">
      <div className="drawer-head">
        <span className="title">{label}</span>
        <span className="muted mono" style={{ fontSize: 12 }}>
          trace #{seq}
        </span>
        <span style={{ flex: 1 }} />
        <button className="btn" onClick={onClose} title="Close (Esc)">
          ✕
        </button>
      </div>
      <Tabs<Tab>
        tabs={[
          { id: "overview", label: "Overview" },
          { id: "input", label: `Input${detail ? ` (${detail.messages.length})` : ""}` },
          { id: "output", label: "Output" },
          { id: "tools", label: `Tools${detail?.tools ? ` (${detail.tools.length})` : ""}` },
          { id: "raw", label: "Raw" },
        ]}
        active={tab}
        onChange={setTab}
      />
      <div className="drawer-body">
        {error && <div className="error-box">{error}</div>}
        {!detail && !error && <div className="muted">Loading…</div>}
        {detail && tab === "overview" && <Overview d={detail} />}
        {detail && tab === "input" && <Input d={detail} />}
        {detail && tab === "output" && <Output d={detail} />}
        {detail && tab === "tools" && <Tools d={detail} />}
        {detail && tab === "raw" && <JsonTree value={detail} />}
      </div>
    </aside>
  );
}

function Stat({ v, l }: { v: string; l: string }) {
  return (
    <div className="stat">
      <div className="v">{v}</div>
      <div className="l">{l}</div>
    </div>
  );
}

function messageSize(m: Message): number {
  return textOf(m).length + reasoningOf(m).length + JSON.stringify(m.tool_calls ?? []).length;
}

function Overview({ d }: { d: LLMCallDetail }) {
  const u = d.usage;
  const byRole = new Map<string, number>();
  for (const m of d.messages) byRole.set(m.role, (byRole.get(m.role) ?? 0) + messageSize(m));
  const toolsSize = JSON.stringify(d.tools ?? []).length;
  const total = [...byRole.values()].reduce((a, b) => a + b, 0) + toolsSize || 1;
  const parts = [...byRole.entries(), ["tools schema", toolsSize] as [string, number]];
  return (
    <>
      <div className="stat-grid">
        <Stat v={d.model ?? "?"} l="model" />
        <Stat v={formatDuration(d.duration_ms)} l="latency" />
        <Stat v={u ? formatTokens(u.input_tokens) : "–"} l="input tokens" />
        <Stat v={u ? formatTokens(u.output_tokens) : "–"} l="output tokens" />
        <Stat v={u?.cache_read ? formatTokens(u.cache_read) : "0"} l="cached input" />
        <Stat v={`${d.messages.length}`} l="messages sent" />
        <Stat v={`+${d.messages.length - d.new_from}`} l="new since last call" />
        <Stat v={`${d.tools?.length ?? 0}`} l="tools offered" />
      </div>
      {d.error && <div className="error-box">{d.error}</div>}
      <div className="section-title">Context composition (by characters)</div>
      <div className="bar">
        {parts.map(([role, n]) => (
          <span
            key={role}
            title={`${role}: ${n} chars`}
            style={{ width: `${(n / total) * 100}%`, background: ROLE_COLORS[role] ?? "var(--orange)" }}
          />
        ))}
      </div>
      <div className="kv">
        {parts.map(([role, n]) => (
          <div key={role} style={{ display: "contents" }}>
            <dt>
              <span style={{ color: ROLE_COLORS[role] ?? "var(--orange)" }}>■</span> {role}
            </dt>
            <dd>
              {n.toLocaleString()} chars · {((n / total) * 100).toFixed(1)}%
            </dd>
          </div>
        ))}
      </div>
      {d.settings && Object.keys(d.settings).length > 0 && (
        <>
          <div className="section-title">Settings</div>
          <JsonTree value={d.settings} />
        </>
      )}
      {d.tool_choice && (
        <div className="muted" style={{ marginTop: 8 }}>
          tool_choice: <span className="mono">{d.tool_choice}</span>
        </div>
      )}
    </>
  );
}

function Input({ d }: { d: LLMCallDetail }) {
  const [onlyNew, setOnlyNew] = useState(false);
  const shown = d.messages.map((m, i) => ({ m, i })).filter(({ i }) => !onlyNew || i >= d.new_from);
  return (
    <>
      <div style={{ display: "flex", gap: 8, alignItems: "center", marginBottom: 10 }}>
        <label className="muted" style={{ fontSize: 12.5 }}>
          <input type="checkbox" checked={onlyNew} onChange={(e) => setOnlyNew(e.target.checked)} /> only messages new
          since the previous call ({d.messages.length - d.new_from})
        </label>
        <span style={{ flex: 1 }} />
        <CopyButton text={JSON.stringify(d.messages, null, 2)} label="Copy messages JSON" />
      </div>
      {shown.map(({ m, i }) => (
        <MessageCard key={i} m={m} index={i} isNew={i >= d.new_from} defaultOpen={i >= d.new_from && m.role !== "system"} />
      ))}
    </>
  );
}

export function MessageCard({
  m,
  index,
  isNew,
  defaultOpen,
}: {
  m: Message;
  index?: number;
  isNew?: boolean;
  defaultOpen: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  const text = textOf(m);
  const reasoning = reasoningOf(m);
  const size = messageSize(m);
  return (
    <div className={`msg ${isNew ? "new" : ""}`}>
      <div className="msg-head" onClick={() => setOpen(!open)}>
        <span className={open ? "caret open" : "caret"}>›</span>
        {index !== undefined && <span className="muted mono">#{index}</span>}
        <span className={`role ${m.role}`}>{m.role}</span>
        {m.name && <span className="mono muted">{m.name}</span>}
        {m.tool_call_id && <span className="mono muted">↩ {m.tool_call_id}</span>}
        {m.status === "error" && <span className="badge bad">error</span>}
        {isNew && <span className="badge new">new</span>}
        {!open && (
          <span className="muted" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", flex: 1 }}>
            {text.slice(0, 120)}
          </span>
        )}
        <span style={{ marginLeft: "auto" }} className="muted mono">
          {size.toLocaleString()} chars
        </span>
      </div>
      {open && (
        <div className="msg-body">
          {reasoning && <div className="reasoning" style={{ margin: "0 0 6px" }}>{reasoning}</div>}
          {text && (m.role === "assistant" ? <Markdown text={text} /> : <pre className="mono">{text}</pre>)}
          {(m.tool_calls ?? []).map((c) => (
            <div className="tool-call-card" key={c.id}>
              <div className="mono" style={{ fontSize: 12 }}>
                <span className="role tool">{c.name}</span> <span className="muted">{c.id}</span>
              </div>
              <JsonTree value={c.args} />
            </div>
          ))}
          {!text && !reasoning && !m.tool_calls?.length && <span className="muted">(empty)</span>}
        </div>
      )}
    </div>
  );
}

function Output({ d }: { d: LLMCallDetail }) {
  if (!d.response) return <div className="muted">{d.error ? `The call failed: ${d.error}` : "No response recorded."}</div>;
  const u = d.response.usage;
  return (
    <>
      {u && (
        <div className="muted mono" style={{ marginBottom: 8, fontSize: 12 }}>
          {u.input_tokens} in / {u.output_tokens} out{u.cache_read ? ` / ${u.cache_read} cached` : ""}
        </div>
      )}
      <MessageCard m={d.response} defaultOpen />
    </>
  );
}

function Tools({ d }: { d: LLMCallDetail }) {
  if (!d.tools?.length) return <div className="muted">No tools were offered.</div>;
  return (
    <>
      {d.tools.map((t) => (
        <ToolSpecCard key={t.name} name={t.name} description={t.description} parameters={t.parameters} />
      ))}
    </>
  );
}

function ToolSpecCard({ name, description, parameters }: { name: string; description: string; parameters: unknown }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="msg">
      <div className="msg-head" onClick={() => setOpen(!open)}>
        <span className={open ? "caret open" : "caret"}>›</span>
        <span className="role tool">{name}</span>
        <span className="muted" style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
          {description.split("\n")[0]}
        </span>
      </div>
      {open && (
        <div className="msg-body">
          <pre className="mono" style={{ marginBottom: 8 }}>
            {description}
          </pre>
          <JsonTree value={parameters} />
        </div>
      )}
    </div>
  );
}
