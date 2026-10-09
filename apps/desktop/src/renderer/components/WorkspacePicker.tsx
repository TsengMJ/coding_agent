import { useState } from "react";
import type { Api } from "../api";
import type { Workspace } from "../types";
import { repoName } from "../model/remote";

// WorkspacePicker chooses the repository a new thread works on: one of the
// user's workspaces, or a fresh clone of a GitHub repository and branch.
export function WorkspacePicker({
  api,
  workspaces,
  onPick,
  onCreated,
}: {
  api: Api;
  workspaces: Workspace[];
  onPick: (ws: Workspace) => void;
  onCreated: (ws: Workspace) => void;
}) {
  const [repo, setRepo] = useState("");
  const [branch, setBranch] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const create = async () => {
    setBusy(true);
    setError("");
    try {
      const ws = await api.createWorkspace({ repo_url: repo.trim(), base_branch: branch.trim() || undefined });
      onCreated(ws);
      onPick(ws);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="workspace-picker">
      <div className="muted">Pick the repository this thread works on. The server clones it into a container; results come back as a pull request.</div>
      {workspaces.length > 0 && (
        <div className="workspace-list">
          {workspaces.map((ws) => (
            <button key={ws.id} className="chip" onClick={() => onPick(ws)} title={`${ws.repo_url} · ${ws.state}`}>
              {repoName(ws.repo_url)}
              {ws.base_branch ? ` @ ${ws.base_branch}` : ""}
            </button>
          ))}
        </div>
      )}
      <form
        className="connect-form"
        onSubmit={(e) => {
          e.preventDefault();
          void create();
        }}
      >
        <input value={repo} onChange={(e) => setRepo(e.target.value)} placeholder="https://github.com/owner/repo" spellCheck={false} aria-label="Repository URL" disabled={busy} />
        <input className="branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="branch (default)" spellCheck={false} aria-label="Base branch" disabled={busy} />
        <button className="btn primary" type="submit" disabled={busy || !repo.trim()}>
          {busy ? "Cloning…" : "Clone"}
        </button>
      </form>
      {error && <div className="note error-note">{error}</div>}
    </div>
  );
}
