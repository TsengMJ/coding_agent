import { app, BrowserWindow, protocol, shell } from "electron";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readAgentConfig } from "./agent.js";
import { APP_ORIGIN, handleAppRequest } from "./protocol.js";

const here = path.dirname(fileURLToPath(import.meta.url));
const rendererDir = path.join(here, "..", "renderer");
// Development only: load the renderer from `npm run dev:renderer`.
const rendererURL = process.env.QUILL_RENDERER_URL;

protocol.registerSchemesAsPrivileged([
  { scheme: "quill", privileges: { standard: true, secure: true, supportFetchAPI: true, stream: true } },
]);

function isAppURL(url: string): boolean {
  return url.startsWith(APP_ORIGIN + "/") || (rendererURL !== undefined && url.startsWith(rendererURL));
}

function createWindow(): void {
  const win = new BrowserWindow({
    width: 1400,
    height: 900,
    title: "quill",
    webPreferences: { contextIsolation: true, nodeIntegration: false, sandbox: true },
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
}

app.whenReady().then(() => {
  protocol.handle("quill", (req) => handleAppRequest(req, { rendererDir, agent: () => readAgentConfig(), fetch }));
  createWindow();
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});
