import { useCallback, useEffect, useMemo, useState } from "react";
import type { Api } from "../api";
import type { PRResult, RepoDiff } from "../types";
import { prTitle, splitPatch, statusLabel, totals } from "../model/remote";
import { DiffView } from "./DiffView";
import { DiffStat } from "./common";

// ChangesPanel is GitHub's "Files changed" for a workspace thread: the
// working tree, untracked files included, against the base branch, and the
// button that delivers it as a pull request.
export function ChangesPanel({
  api,
  threadId,
  firstRequest,
  running,
  onClose,
}: {
  api: Api;
  threadId: string;
  firstRequest: string;
  running: boolean;
  onClose: () => void;
}) {
  const [diff, setDiff] = useState<RepoDiff | null>(null);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState("");
  const [title, setTitle] = useState(() => prTitle(firstRequest));
  const [body, setBody] = useState("");
  const [draft, setDraft] = useState(false);
  const [creating, setCreating] = useState(false);
  const [pr, setPR] = useState<PRResult | null>(null);

  const load = useCallback(async () => {
    setError("");
    try {
      setDiff(await api.diff(threadId));
    } catch (e) {
      setError((e as Error).message);
    }
  }, [api, threadId]);

  // Reload when a run finishes, since it is what changes the files.
  useEffect(() => {
    if (!running) void load();
  }, [load, running]);

  const patches = useMemo(() => splitPatch(diff?.patch ?? ""), [diff]);
  const files = diff?.files ?? [];
  const sum = totals(files);
  const shown = files.find((f) => f.path === selected) ?? files[0];

  const create = async () => {
    setCreating(true);
    setError("");
    try {
      setPR(await api.createPR(threadId, { title: title.trim(), body: body.trim() || undefined, draft }));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setCreating(false);
    }
  };

  return (
    <aside className="browser-pane changes-pane" aria-label="Files changed">
      <div className="browser-bar">
        <b className="grow">Files changed</b>
        {diff && (
          <span className="muted">
            {files.length} files <DiffStat added={sum.additions} removed={sum.deletions} />
          </span>
        )}
        <button className="chip" onClick={() => void load()} title="Reload" aria-label="Reload">
          ↻
        </button>
        <button className="chip" onClick={onClose} title="Hide" aria-label="Hide">
          ×
        </button>
      </div>
      <div className="changes-body">
        {error && <div className="note error-note">{error}</div>}
        {diff && files.length === 0 && <div className="muted" style={{ padding: 12 }}>No changes yet.</div>}
        {files.length > 0 && (
          <ul className="changes-list">
            {files.map((f) => (
              <li key={f.path}>
                <button className={`change ${shown?.path === f.path ? "active" : ""}`} onClick={() => setSelected(f.path)} title={f.old_path ? `${f.old_path} → ${f.path}` : f.path}>
                  <span className={`status status-${f.status}`}>{statusLabel(f.status)}</span>
                  <span className="grow">{f.path}</span>
                  {f.binary ? <span className="muted">binary</span> : <DiffStat added={f.additions} removed={f.deletions} />}
                </button>
              </li>
            ))}
          </ul>
        )}
        {shown && !shown.binary && <DiffView unified={patches.get(shown.path) ?? ""} path={shown.path} added={shown.additions} removed={shown.deletions} />}
        {diff?.truncated && <div className="note">The patch is too large to show in full.</div>}
        {files.length > 0 && (
          <form
            className="pr-form"
            onSubmit={(e) => {
              e.preventDefault();
              void create();
            }}
          >
            <input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Pull request title" aria-label="Pull request title" />
            <textarea value={body} onChange={(e) => setBody(e.target.value)} placeholder="Description (the task and changed files are added)" rows={3} aria-label="Pull request description" />
            <label className="muted">
              <input type="checkbox" checked={draft} onChange={(e) => setDraft(e.target.checked)} /> draft
            </label>
            <button className="btn primary" type="submit" disabled={creating || running || !title.trim()} title={running ? "Wait for the run to finish" : "Commit, push quill/<thread> and open a pull request as you"}>
              {creating ? "Opening…" : "Create pull request"}
            </button>
          </form>
        )}
        {pr && (
          <div className="note">
            {pr.pull_request.existing ? "Updated" : "Opened"} pull request{" "}
            <a href={pr.pull_request.html_url} target="_blank" rel="noreferrer">
              #{pr.pull_request.number}
            </a>{" "}
            from <span className="mono">{pr.commit.branch}</span>.
          </div>
        )}
      </div>
    </aside>
  );
}
