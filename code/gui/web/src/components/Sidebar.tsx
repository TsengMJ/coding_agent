import { useState } from "react";
import type { ThreadInfo } from "../types";

function ago(iso?: string): string {
  if (!iso) return "";
  const s = (Date.now() - new Date(iso).getTime()) / 1000;
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function Sidebar({
  threads,
  current,
  allDirs,
  onAllDirs,
  onPick,
  onNew,
  onRename,
  version,
}: {
  threads: ThreadInfo[];
  current: string;
  allDirs: boolean;
  onAllDirs: (v: boolean) => void;
  onPick: (t: ThreadInfo) => void;
  onNew: () => void;
  onRename: (id: string, title: string) => Promise<boolean>;
  version: string;
}) {
  const [editing, setEditing] = useState("");
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const runningByCwd = new Map<string, number>();
  for (const thread of threads) {
    if (thread.running && thread.cwd) runningByCwd.set(thread.cwd, (runningByCwd.get(thread.cwd) ?? 0) + 1);
  }
  const listed: ThreadInfo[] = threads.some((t) => t.thread_id === current) ? threads : [{ thread_id: current, cwd: "", title: "New thread" }, ...threads];
  const save = async () => {
    if (!draft.trim() || saving) return;
    const id = editing;
    setSaving(true);
    try {
      if (await onRename(id, draft.trim())) setEditing((current) => current === id ? "" : current);
    } finally {
      setSaving(false);
    }
  };
  return (
    <nav className="sidebar">
      <div className="sidebar-head">
        <span className="brand">dcode</span>
        <button className="btn" onClick={onNew} title="Start a new thread">
          + New
        </button>
      </div>
      <div style={{ padding: "6px 12px" }}>
        <label className="muted" style={{ fontSize: 12 }}>
          <input type="checkbox" checked={allDirs} onChange={(e) => onAllDirs(e.target.checked)} /> all directories
        </label>
      </div>
      <div className="thread-list">
        {listed.map((t) => (
          <div key={t.thread_id} className={`thread-item ${t.thread_id === current ? "active" : ""}`} title={t.cwd}>
            {editing === t.thread_id ? (
              <form className="thread-rename" onSubmit={(e) => { e.preventDefault(); void save(); }}>
                <input
                  autoFocus
                  aria-label="Thread name"
                  maxLength={120}
                  value={draft}
                  onChange={(e) => setDraft(e.target.value)}
                  onKeyDown={(e) => { if (e.key === "Escape") setEditing(""); }}
                />
                <button type="submit" disabled={saving || !draft.trim()} aria-label="Save name" title="Save name">✓</button>
                <button type="button" onClick={() => setEditing("")} aria-label="Cancel rename" title="Cancel rename">×</button>
              </form>
            ) : (
              <div className="thread-item-top">
                <button className="thread-pick" onClick={() => onPick(t)}>
                  <span className="thread-title" title={t.title || "(untitled)"}>{t.title || "(untitled)"}</span>
                </button>
                {t.running && <span className="thread-running" title="This thread is currently running"><span className="spinner" /> Running</span>}
                {t.updated_at && (
                  <button
                    className="thread-edit"
                    aria-label={`Rename ${t.title || "thread"}`}
                    title="Rename thread"
                    onClick={() => { setEditing(t.thread_id); setDraft(t.title || ""); }}
                  >
                    ✎
                  </button>
                )}
              </div>
            )}
            <div className="thread-meta">
              {t.thread_id} {t.message_count ? `· ${t.message_count} msgs` : ""} {ago(t.updated_at)}
            </div>
            {allDirs && t.cwd && <div className="thread-meta mono">{t.cwd}</div>}
            {t.running && (runningByCwd.get(t.cwd) ?? 0) > 1 && <div className="thread-warning">⚠ multiple agents share this directory</div>}
          </div>
        ))}
      </div>
      <div className="sidebar-foot">dcode (go) {version}</div>
    </nav>
  );
}
