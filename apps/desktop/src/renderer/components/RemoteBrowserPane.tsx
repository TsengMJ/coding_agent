import { useEffect, useRef, useState, type MouseEvent } from "react";
import type { Api } from "../api";
import { toPageCoords } from "../model/remote";

const FRAME_INTERVAL_MS = 1000;

// RemoteBrowserPane shows the headless browser in the thread's workspace
// container as a stream of screenshots; clicks on the picture are clicked
// on the page.
export function RemoteBrowserPane({ api, threadId, onClose }: { api: Api; threadId: string; onClose: () => void }) {
  const [frame, setFrame] = useState("");
  const [error, setError] = useState("");
  const [waiting, setWaiting] = useState(true);
  const imgRef = useRef<HTMLImageElement>(null);
  const refreshRef = useRef<() => void>(() => undefined);

  useEffect(() => {
    let stopped = false;
    let url = "";
    let timer = 0;
    let ctrl: AbortController | undefined;
    const tick = async () => {
      window.clearTimeout(timer);
      ctrl?.abort();
      ctrl = new AbortController();
      if (document.visibilityState === "visible") {
        try {
          const blob = await api.screenshot(threadId, ctrl.signal);
          if (stopped) return;
          setWaiting(blob === null);
          setError("");
          if (blob) {
            const next = URL.createObjectURL(blob);
            if (url) URL.revokeObjectURL(url);
            url = next;
            setFrame(next);
          }
        } catch (e) {
          if (stopped || (e as Error).name === "AbortError") return;
          setError((e as Error).message);
        }
      }
      if (!stopped) timer = window.setTimeout(() => void tick(), FRAME_INTERVAL_MS);
    };
    refreshRef.current = () => void tick();
    void tick();
    return () => {
      stopped = true;
      window.clearTimeout(timer);
      ctrl?.abort();
      if (url) URL.revokeObjectURL(url);
    };
  }, [api, threadId]);

  const click = (e: MouseEvent<HTMLImageElement>) => {
    const img = imgRef.current;
    if (!img) return;
    const at = toPageCoords(
      { x: e.nativeEvent.offsetX, y: e.nativeEvent.offsetY },
      { width: img.clientWidth, height: img.clientHeight },
      { width: img.naturalWidth, height: img.naturalHeight },
    );
    if (!at) return;
    api
      .click(threadId, at.x, at.y)
      .then(() => refreshRef.current())
      .catch((err: Error) => setError(err.message));
  };

  return (
    <aside className="browser-pane" aria-label="Workspace browser">
      <div className="browser-bar">
        <b className="grow">Workspace browser</b>
        <button className="chip" onClick={() => refreshRef.current()} title="Refresh" aria-label="Refresh">
          ↻
        </button>
        <button className="chip" onClick={onClose} title="Hide the browser" aria-label="Hide the browser">
          ×
        </button>
      </div>
      <div className="browser-view remote-browser">
        {error && <div className="note error-note">{error}</div>}
        {frame && !waiting ? (
          <img ref={imgRef} src={frame} alt="The page in the workspace browser" onClick={click} draggable={false} />
        ) : (
          <div className="browser-empty muted">The browser in this thread's container starts with its first run; the agent's pages show here.</div>
        )}
      </div>
    </aside>
  );
}
