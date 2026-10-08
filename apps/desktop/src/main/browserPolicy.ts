import fs from "node:fs";
import path from "node:path";
const LOCAL_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

// isAllowedPanelURL reports whether the browser panel may open url: only
// local dev servers, so the agent cannot browse the web or reach the
// user's logged-in sites through it.
export function isAllowedPanelURL(url: string): boolean {
  if (url === "about:blank") return true;
  let u: URL;
  try {
    u = new URL(url);
  } catch {
    return false;
  }
  return (u.protocol === "http:" || u.protocol === "https:") && LOCAL_HOSTS.has(u.hostname);
}

// readDevToolsPort returns the port Chromium picked for
// --remote-debugging-port=0, or null until it has written the file.
export function readDevToolsPort(userDataDir: string): number | null {
  try {
    const port = Number(fs.readFileSync(path.join(userDataDir, "DevToolsActivePort"), "utf8").split("\n")[0]);
    return Number.isInteger(port) && port > 0 ? port : null;
  } catch {
    return null;
  }
}

// registerBrowser tells the agent where the panel's filtered DevTools
// endpoint is; an empty cdpURL unregisters it.
export async function registerBrowser(
  agent: { url: string; token: string },
  cdpURL: string,
  fetchFn: typeof fetch = fetch,
): Promise<void> {
  const resp = await fetchFn(agent.url + "/browser", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-Quill-Token": agent.token },
    body: JSON.stringify({ cdp_url: cdpURL }),
  });
  if (!resp.ok) throw new Error(`${resp.status}: ${(await resp.text()).trim()}`);
}
