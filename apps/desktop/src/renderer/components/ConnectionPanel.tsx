import { useState } from "react";
import type { ConnectionState, DesktopConnection } from "../desktop";
import { CopyButton } from "./common";

// ConnectionPanel picks where the agent runs: the `quill serve` on this
// computer, or a team server signed in to with GitHub.
export function ConnectionPanel({
  connection,
  state,
  message,
  onChanged,
  onClose,
}: {
  connection: DesktopConnection;
  state: ConnectionState | null;
  message?: string;
  onChanged: (s: ConnectionState) => void;
  onClose?: () => void;
}) {
  const [url, setURL] = useState(state?.mode === "remote" ? state.url : "");
  const [code, setCode] = useState<{ user_code: string; verification_uri: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const act = async (fn: () => Promise<ConnectionState>) => {
    setBusy(true);
    setError("");
    try {
      onChanged(await fn());
    } catch (e) {
      setError((e as Error).message.replace(/^Error invoking remote method '[^']+': (Error: )?/, ""));
    } finally {
      setBusy(false);
      setCode(null);
    }
  };

  const signIn = () =>
    act(async () => {
      setCode(await connection.login(url));
      return connection.wait();
    });

  const signedIn = state?.mode === "remote" && state.signedIn;
  return (
    <div className="connect-panel">
      <h2>Server</h2>
      {message && <div className="note">{message}</div>}
      {signedIn ? (
        <p>
          Signed in to <b>{state.url}</b>
          {state.user ? ` as ${state.user}` : ""}. Threads run in containers on that server.
        </p>
      ) : (
        <p className="muted">
          {state?.mode === "local"
            ? "Using the quill serve on this computer. Sign in to a team server to run threads there instead."
            : "Sign in with GitHub to use the team server."}
        </p>
      )}
      {!signedIn && (
        <form
          className="connect-form"
          onSubmit={(e) => {
            e.preventDefault();
            void signIn();
          }}
        >
          <input value={url} onChange={(e) => setURL(e.target.value)} placeholder="https://quill.example.com" spellCheck={false} aria-label="Server URL" disabled={busy} />
          <button className="btn primary" type="submit" disabled={busy || !url.trim()}>
            Sign in with GitHub
          </button>
        </form>
      )}
      {code && (
        <div className="note">
          Enter <b className="mono">{code.user_code}</b> <CopyButton text={code.user_code} label="⧉" /> at{" "}
          <a href={code.verification_uri} target="_blank" rel="noreferrer">
            {code.verification_uri}
          </a>{" "}
          (opened in your browser). Waiting for GitHub…
        </div>
      )}
      {error && <div className="note error-note">{error}</div>}
      <div className="connect-actions">
        {signedIn && (
          <button className="btn" disabled={busy} onClick={() => void act(() => connection.logout())}>
            Sign out
          </button>
        )}
        {state?.mode === "remote" && (
          <button className="btn" disabled={busy} onClick={() => void act(() => connection.useLocal())}>
            Use this computer instead
          </button>
        )}
        {onClose && (
          <button className="btn" onClick={onClose}>
            Close
          </button>
        )}
      </div>
    </div>
  );
}
