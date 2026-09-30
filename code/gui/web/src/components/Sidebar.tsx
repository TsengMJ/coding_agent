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
  version,
}: {
  threads: ThreadInfo[];
  current: string;
  allDirs: boolean;
  onAllDirs: (v: boolean) => void;
  onPick: (t: ThreadInfo) => void;
  onNew: () => void;
  version: string;
}) {
  const listed = threads.some((t) => t.thread_id === current) ? threads : [{ thread_id: current, cwd: "", title: "New thread" }, ...threads];
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
          <button
            key={t.thread_id}
            className={`thread-item ${t.thread_id === current ? "active" : ""}`}
            onClick={() => onPick(t)}
            title={t.cwd}
          >
            <div className="thread-title">{t.title || "(untitled)"}</div>
            <div className="thread-meta">
              {t.thread_id} {t.message_count ? `· ${t.message_count} msgs` : ""} {ago(t.updated_at)}
            </div>
            {allDirs && t.cwd && <div className="thread-meta mono">{t.cwd}</div>}
          </button>
        ))}
      </div>
      <div className="sidebar-foot">dcode (go) {version}</div>
    </nav>
  );
}
