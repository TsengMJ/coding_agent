import { app, type BrowserWindow, ipcMain, type IpcMainEvent, session, WebContentsView } from "electron";
import type { PanelRef } from "./cdpFilter.js";
import { isAllowedPanelURL } from "./browserPolicy.js";

// The panel's own in-memory session: no cookies shared with the quill UI
// and nothing kept after the app quits. Its browser context is what the
// CDP filter exposes.
const PARTITION = "quill-browser";

export interface PanelState {
  url: string;
  title: string;
  loading: boolean;
  canGoBack: boolean;
}

export interface Rect {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface BrowserPanel {
  ref: PanelRef;
  view: WebContentsView;
}

// createBrowserPanel adds the panel to win. The renderer places it with
// "browser:bounds" (null hides it) and drives it with the other
// "browser:*" messages; only win's own page may send them.
export async function createBrowserPanel(win: BrowserWindow, isAppURL: (url: string) => boolean): Promise<BrowserPanel> {
  const ses = session.fromPartition(PARTITION);
  // Top-level and frame navigations stay on local dev servers; pages may
  // still load scripts, styles and fonts from elsewhere.
  ses.webRequest.onBeforeRequest({ urls: ["<all_urls>"] }, (details, callback) => {
    const navigation = details.resourceType === "mainFrame" || details.resourceType === "subFrame";
    callback({ cancel: navigation && !isAllowedPanelURL(details.url) });
  });
  ses.setPermissionRequestHandler((_wc, _permission, callback) => callback(false));

  // backgroundThrottling off keeps the panel rendering while the window is
  // behind other apps; without frames, Playwright's clicks time out
  // waiting for elements to settle.
  const view = new WebContentsView({
    webPreferences: { partition: PARTITION, contextIsolation: true, nodeIntegration: false, sandbox: true, backgroundThrottling: false },
  });
  const wc = view.webContents;
  const block = (event: Electron.Event, url: string) => {
    if (!isAllowedPanelURL(url)) event.preventDefault();
  };
  wc.on("will-navigate", block);
  wc.on("will-redirect", block);
  wc.setWindowOpenHandler(({ url }) => {
    if (isAllowedPanelURL(url)) void wc.loadURL(url);
    return { action: "deny" };
  });
  view.setVisible(false);
  win.contentView.addChildView(view);
  await wc.loadURL("about:blank");

  const fromApp = (e: IpcMainEvent) => e.sender === win.webContents && isAppURL(e.senderFrame?.url ?? "");
  const sendState = () => {
    if (win.isDestroyed()) return;
    const state: PanelState = { url: wc.getURL(), title: wc.getTitle(), loading: wc.isLoading(), canGoBack: wc.navigationHistory.canGoBack() };
    win.webContents.send("browser:state", state);
  };
  // A navigation can swap in a new renderer widget that starts out hidden;
  // re-applying the setting shows it again.
  wc.on("did-navigate", () => wc.setBackgroundThrottling(false));
  for (const ev of ["did-navigate", "did-navigate-in-page", "page-title-updated", "did-start-loading", "did-stop-loading"] as const) {
    wc.on(ev as "did-navigate", sendState);
  }
  ipcMain.on("browser:bounds", (e, rect: Rect | null) => {
    if (!fromApp(e)) return;
    if (!rect) {
      view.setVisible(false);
      return;
    }
    view.setBounds({ x: Math.round(rect.x), y: Math.round(rect.y), width: Math.round(rect.width), height: Math.round(rect.height) });
    view.setVisible(true);
  });
  ipcMain.on("browser:navigate", (e, url: string) => {
    if (fromApp(e) && typeof url === "string" && isAllowedPanelURL(url)) void wc.loadURL(url);
  });
  ipcMain.on("browser:back", (e) => {
    if (fromApp(e) && wc.navigationHistory.canGoBack()) wc.navigationHistory.goBack();
  });
  ipcMain.on("browser:reload", (e) => {
    if (fromApp(e)) wc.reload();
  });
  ipcMain.on("browser:hello", (e) => {
    if (fromApp(e)) sendState();
  });

  wc.debugger.attach("1.3");
  const { targetInfo } = (await wc.debugger.sendCommand("Target.getTargetInfo")) as { targetInfo: { targetId: string; browserContextId: string } };
  wc.debugger.detach();
  app.on("before-quit", () => ses.clearStorageData().catch(() => undefined));
  return { ref: { targetId: targetInfo.targetId, contextId: targetInfo.browserContextId }, view };
}
