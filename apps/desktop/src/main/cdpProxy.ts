// A DevTools endpoint for browser MCP servers that exposes only the browser
// panel (see cdpFilter.ts). It listens on 127.0.0.1 and forwards to
// Electron's own remote debugging port.

import http from "node:http";
import type { AddressInfo } from "node:net";
import { WebSocket, WebSocketServer, type RawData } from "ws";
import { CdpFilter, type PanelRef } from "./cdpFilter.js";

export interface CdpProxy {
  url: string;
  close(): Promise<void>;
}

export interface CdpProxyOptions {
  // upstream is Electron's DevTools HTTP endpoint, e.g. http://127.0.0.1:9222.
  upstream: string;
  panel: PanelRef;
}

const DISCOVERY = new Set(["/json/version", "/json/list", "/json", "/json/protocol"]);

export async function startCdpProxy(opts: CdpProxyOptions): Promise<CdpProxy> {
  const upstreamHost = new URL(opts.upstream).host;
  const server = http.createServer((req, res) => {
    void serveDiscovery(req, res);
  });
  const selfHost = () => `127.0.0.1:${(server.address() as AddressInfo).port}`;

  async function serveDiscovery(req: http.IncomingMessage, res: http.ServerResponse): Promise<void> {
    const path = new URL(req.url ?? "/", "http://x").pathname.replace(/\/$/, "");
    if (req.method !== "GET" || !DISCOVERY.has(path)) {
      res.writeHead(404).end();
      return;
    }
    try {
      const upstream = await fetch(opts.upstream + (path === "/json" ? "/json/list" : path));
      let data = await upstream.json();
      if (Array.isArray(data)) data = data.filter((t: { id?: string }) => t.id === opts.panel.targetId);
      const body = JSON.stringify(data).replaceAll(upstreamHost, selfHost());
      res.writeHead(upstream.status, { "content-type": "application/json" }).end(body);
    } catch (e) {
      res.writeHead(502).end(`cannot reach the browser: ${(e as Error).message}`);
    }
  }

  const wss = new WebSocketServer({ noServer: true });
  server.on("upgrade", (req, socket, head) => {
    const path = new URL(req.url ?? "/", "http://x").pathname;
    const filtered = path.startsWith("/devtools/browser/");
    if (!filtered && path !== `/devtools/page/${opts.panel.targetId}`) {
      socket.end("HTTP/1.1 403 Forbidden\r\n\r\n");
      return;
    }
    wss.handleUpgrade(req, socket, head, (client) => bridge(client, path, filtered));
  });

  function bridge(client: WebSocket, path: string, filtered: boolean): void {
    const upstream = new WebSocket(opts.upstream.replace(/^http/, "ws") + path, { perMessageDeflate: false, maxPayload: 256 * 1024 * 1024 });
    const send = (ws: WebSocket, data: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data);
    };
    const closeBoth = () => {
      client.close();
      upstream.close();
    };
    upstream.on("close", closeBoth);
    upstream.on("error", closeBoth);
    client.on("close", closeBoth);
    client.on("error", closeBoth);

    if (!filtered) {
      // A page connection is already scoped to the panel.
      const pending: RawData[] = [];
      client.on("message", (raw) => (upstream.readyState === WebSocket.OPEN ? upstream.send(raw) : pending.push(raw)));
      upstream.on("open", () => pending.splice(0).forEach((raw) => upstream.send(raw)));
      upstream.on("message", (raw) => send(client, raw.toString()));
      return;
    }
    const filter = new CdpFilter(opts.panel, {
      toUpstream: (m) => send(upstream, JSON.stringify(m)),
      toClient: (m) => send(client, JSON.stringify(m)),
    });
    const parse = (raw: RawData) => {
      try {
        return JSON.parse(raw.toString());
      } catch {
        closeBoth();
        return null;
      }
    };
    client.on("message", (raw) => {
      const m = parse(raw);
      if (m) filter.fromClient(m);
    });
    upstream.on("message", (raw) => {
      const m = parse(raw);
      if (m) filter.fromUpstream(m);
    });
    upstream.on("open", () => {
      filter.start().catch(closeBoth);
    });
  }

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    url: `http://${selfHost()}`,
    close: () =>
      new Promise<void>((resolve) => {
        for (const c of wss.clients) c.terminate();
        wss.close();
        server.closeAllConnections();
        server.close(() => resolve());
      }),
  };
}
