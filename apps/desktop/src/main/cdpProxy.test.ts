import http from "node:http";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import { WebSocket, WebSocketServer } from "ws";
import { startCdpProxy, type CdpProxy } from "./cdpProxy.js";

const panel = { targetId: "PANEL", contextId: "CTX-PANEL" };
const targets = [
  { targetId: "PANEL", type: "page", title: "Panel", browserContextId: "CTX-PANEL" },
  { targetId: "GUI", type: "page", title: "quill", browserContextId: "CTX-GUI" },
];

// fakeChrome serves the DevTools HTTP discovery endpoints and a browser
// WebSocket that answers Target.getTargets.
async function fakeChrome() {
  const closed: string[] = [];
  const server = http.createServer((req, res) => {
    const host = req.headers.host;
    if (req.url === "/json/version") {
      res.end(JSON.stringify({ Browser: "Chrome/1", webSocketDebuggerUrl: `ws://${host}/devtools/browser/B` }));
    } else if (req.url === "/json/list") {
      res.end(JSON.stringify(targets.map((t) => ({ id: t.targetId, title: t.title, type: t.type, webSocketDebuggerUrl: `ws://${host}/devtools/page/${t.targetId}` }))));
    } else if (req.url?.startsWith("/json/close/")) {
      closed.push(req.url);
      res.end("Target is closing");
    } else {
      res.statusCode = 404;
      res.end();
    }
  });
  const wss = new WebSocketServer({ server });
  wss.on("connection", (ws) => {
    ws.on("message", (raw) => {
      const m = JSON.parse(raw.toString());
      if (m.method === "Target.getTargets") ws.send(JSON.stringify({ id: m.id, result: { targetInfos: targets } }));
      else ws.send(JSON.stringify({ id: m.id, result: { echoed: m.method } }));
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const url = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  return { url, closed, close: () => new Promise<void>((r) => { wss.close(); server.close(() => r()); }) };
}

const cleanups: (() => Promise<void>)[] = [];
afterEach(async () => {
  for (const c of cleanups.splice(0).reverse()) await c();
});

async function setup(): Promise<{ proxy: CdpProxy; chrome: Awaited<ReturnType<typeof fakeChrome>> }> {
  const chrome = await fakeChrome();
  cleanups.push(chrome.close);
  const proxy = await startCdpProxy({ upstream: chrome.url, panel });
  cleanups.push(proxy.close);
  return { proxy, chrome };
}

function connect(url: string): Promise<WebSocket> {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(url);
    ws.on("open", () => resolve(ws));
    ws.on("error", reject);
    ws.on("unexpected-response", (_req, res) => reject(new Error(`status ${res.statusCode}`)));
  });
}

function request(ws: WebSocket, msg: object): Promise<{ result?: { targetInfos?: { targetId: string }[] }; error?: { message: string } }> {
  return new Promise((resolve) => {
    ws.once("message", (raw) => resolve(JSON.parse(raw.toString())));
    ws.send(JSON.stringify(msg));
  });
}

describe("startCdpProxy", () => {
  it("rewrites /json/version to point at the proxy", async () => {
    const { proxy } = await setup();
    const v = await fetch(proxy.url + "/json/version/").then((r) => r.json());
    expect(v.webSocketDebuggerUrl).toBe(proxy.url.replace("http", "ws") + "/devtools/browser/B");
  });

  it("lists only the panel page", async () => {
    const { proxy } = await setup();
    const list = await fetch(proxy.url + "/json/list").then((r) => r.json());
    expect(list.map((t: { id: string }) => t.id)).toEqual(["PANEL"]);
    expect(list[0].webSocketDebuggerUrl).toBe(proxy.url.replace("http", "ws") + "/devtools/page/PANEL");
  });

  it("refuses other HTTP endpoints such as /json/close", async () => {
    const { proxy, chrome } = await setup();
    const res = await fetch(proxy.url + "/json/close/GUI");
    expect(res.status).toBe(404);
    expect(chrome.closed).toEqual([]);
  });

  it("refuses a WebSocket to a page other than the panel", async () => {
    const { proxy } = await setup();
    await expect(connect(proxy.url.replace("http", "ws") + "/devtools/page/GUI")).rejects.toThrow("403");
  });

  it("filters the browser WebSocket", async () => {
    const { proxy } = await setup();
    const ws = await connect(proxy.url.replace("http", "ws") + "/devtools/browser/B");
    cleanups.push(async () => ws.close());
    const targetsReply = await request(ws, { id: 1, method: "Target.getTargets", params: {} });
    expect(targetsReply.result?.targetInfos?.map((t) => t.targetId)).toEqual(["PANEL"]);
    const attach = await request(ws, { id: 2, method: "Target.attachToTarget", params: { targetId: "GUI" } });
    expect(attach.error?.message).toBe("target not allowed");
  });
});
