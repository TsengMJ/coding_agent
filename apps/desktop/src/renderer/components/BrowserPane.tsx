import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { DesktopBrowser, PanelState } from "../desktop";
import { displayURL, hasPage, normalizeTypedURL } from "../model/browser";

// BrowserPane frames the Electron browser panel: the panel itself is a
// native view that main places over the .browser-view element.
export function BrowserPane({
  browser,
  state,
  kinds,
  kind,
  connected,
  onKind,
  onClose,
}: {
  browser: DesktopBrowser;
  state: PanelState;
  kinds: string[];
  kind: string;
  connected: boolean;
  onKind: (k: string) => void;
  onClose: () => void;
}) {
  const viewRef = useRef<HTMLDivElement>(null);
  const [address, setAddress] = useState(displayURL(state.url));

  useEffect(() => setAddress(displayURL(state.url)), [state.url]);

  const showView = hasPage(state);
  useLayoutEffect(() => {
    const el = viewRef.current;
    if (!el || !showView) return;
    const place = () => {
      const r = el.getBoundingClientRect();
      browser.setBounds({ x: r.left, y: r.top, width: r.width, height: r.height });
    };
    place();
    const ro = new ResizeObserver(place);
    ro.observe(el);
    window.addEventListener("resize", place);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", place);
      browser.setBounds(null);
    };
  }, [browser, showView]);

  return (
    <aside className="browser-pane" aria-label="Browser">
      <div className="browser-bar">
        <button className="chip" onClick={() => browser.back()} disabled={!state.canGoBack} title="Back" aria-label="Back">
          ←
        </button>
        <button className="chip" onClick={() => browser.reload()} title="Reload" aria-label="Reload">
          {state.loading ? "…" : "↻"}
        </button>
        <form
          className="browser-address"
          onSubmit={(e) => {
            e.preventDefault();
            const url = normalizeTypedURL(address);
            if (url) browser.navigate(url);
          }}
        >
          <input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="http://localhost:3000" spellCheck={false} aria-label="Address" />
        </form>
        <select
          className="chip"
          value={kind}
          onChange={(e) => onKind(e.target.value)}
          title={connected ? "MCP server the agent uses to drive this panel (next run)" : "The agent service has not registered this panel yet"}
          aria-label="Browser MCP"
        >
          {kinds.map((k) => (
            <option key={k} value={k}>
              {k}
            </option>
          ))}
        </select>
        <button className="chip" onClick={onClose} title="Hide the browser" aria-label="Hide the browser">
          ×
        </button>
      </div>
      <div ref={viewRef} className="browser-view">
        <div className="browser-empty muted">The agent opens local pages here (http://localhost, http://127.0.0.1).</div>
      </div>
    </aside>
  );
}
