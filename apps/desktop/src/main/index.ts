import { app, BrowserWindow, ipcMain, protocol, safeStorage, shell } from "electron";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readAgentConfig } from "./agent.js";
import { createBrowserPanel } from "./browserPanel.js";
import { readDevToolsPort, registerBrowser } from "./browserPolicy.js";
import { startCdpProxy, type CdpProxy } from "./cdpProxy.js";
import { Connections } from "./connection.js";
import { APP_ORIGIN, handleAppRequest } from "./protocol.js";
import { startAutoUpdate } from "./updates.js";

const here = path.dirname(fileURLToPath(import.meta.url));
const rendererDir = path.join(here, "..", "renderer");
// Development only: load the renderer from `npm run dev:renderer`.
const rendererURL = process.env.QUILL_RENDERER_URL;

// Browser MCP servers reach the panel through Chromium's DevTools port,
// on loopback and a random port; they only ever get the filtered proxy.
const devToolsPortFile = path.join(app.getPath("userData"), "DevToolsActivePort");
fs.rmSync(devToolsPortFile, { force: true });
app.commandLine.appendSwitch("remote-debugging-port", "0");
app.commandLine.appendSwitch("remote-debugging-address", "127.0.0.1");

protocol.registerSchemesAsPrivileged([
  { scheme: "quill", privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true } },
]);

function isAppURL(url: string): boolean {
  return url.startsWith(APP_ORIGIN + "/") || (rendererURL !== undefined && url.startsWith(rendererURL));
}

let proxy: CdpProxy | undefined;
let connections: Connections;

// The renderer can see and change where the agent runs, and start the
// GitHub login; it never sees a session token.
function handleConnections(): void {
  ipcMain.handle("connection:get", () => connections.state());
  ipcMain.handle("connection:local", () => connections.useLocal());
  ipcMain.handle("connection:login", async (_e, url: unknown) => {
    const code = await connections.login(String(url));
    if (code.verification_uri.startsWith("https://")) void shell.openExternal(code.verification_uri);
    return { user_code: code.user_code, verification_uri: code.verification_uri };
  });
  ipcMain.handle("connection:wait", () => connections.wait());
  ipcMain.handle("connection:logout", () => connections.logout());
}

function createWindow(): BrowserWindow {
  const win = new BrowserWindow({
    width: 1600,
    height: 950,
    title: "quill",
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true, preload: path.join(here, "preload.cjs") },
  });
  // Links in agent output open in the system browser, never inside the app.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (url.startsWith("https://") || url.startsWith("http://")) void shell.openExternal(url);
    return { action: "deny" };
  });
  win.webContents.on("will-navigate", (event, url) => {
    if (!isAppURL(url)) event.preventDefault();
  });
  void win.loadURL(rendererURL ?? APP_ORIGIN + "/");
  return win;
}

async function waitForDevToolsPort(): Promise<number> {
  for (let i = 0; i < 100; i++) {
    const port = readDevToolsPort(app.getPath("userData"));
    if (port) return port;
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`Chromium did not write ${devToolsPortFile}`);
}

// register tells the agent about the panel. It runs whenever the UI
// (re)loads, which is also how a restarted `quill serve` learns about it.
async function register(cdpURL: string): Promise<void> {
  // A team server runs its own browser in each workspace container.
  const agent = connections.agent();
  if ("error" in agent || agent.remote) return;
  try {
    await registerBrowser(agent, cdpURL);
  } catch (e) {
    console.error(`quill: cannot register the browser panel with the agent: ${(e as Error).message}`);
  }
}

async function setupBrowser(win: BrowserWindow): Promise<void> {
  const panel = await createBrowserPanel(win, isAppURL);
  const port = await waitForDevToolsPort();
  proxy = await startCdpProxy({ upstream: `http://127.0.0.1:${port}`, panel: panel.ref });
  const url = proxy.url;
  win.webContents.on("did-finish-load", () => void register(url));
  await register(url);
}

if (!app.requestSingleInstanceLock()) {
  app.quit();
} else {
  app.whenReady().then(() => {
    connections = new Connections({ dir: app.getPath("userData"), crypt: safeStorage, fetch, local: () => readAgentConfig() });
    handleConnections();
    protocol.handle("quill", (req) =>
      handleAppRequest(req, { rendererDir, agent: () => connections.agent(), fetch, onUnauthorized: (url) => connections.unauthorized(url) }),
    );
    const win = createWindow();
    startAutoUpdate();
    app.on("second-instance", () => {
      if (win.isMinimized()) win.restore();
      win.focus();
    });
    setupBrowser(win).catch((e) => console.error(`quill: browser panel unavailable: ${(e as Error).message}`));
  });

  let unregistered = false;
  app.on("before-quit", (event) => {
    if (unregistered || !proxy) return;
    event.preventDefault();
    unregistered = true;
    let quitting = false;
    const done = () => {
      if (quitting) return;
      quitting = true;
      void proxy?.close();
      app.quit();
    };
    void register("").finally(done);
    setTimeout(done, 1000);
  });

  app.on("window-all-closed", () => app.quit());
}
