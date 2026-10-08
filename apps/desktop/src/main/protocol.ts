import fs from "node:fs/promises";
import path from "node:path";
import type { AgentConfig } from "./agent.js";

export const APP_ORIGIN = "quill://app";

export type AppDeps = {
  rendererDir: string;
  // Read per request so restarting `quill serve` (new token) needs no app restart.
  agent: () => AgentConfig;
  fetch: typeof fetch;
};

const CONTENT_TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".woff2": "font/woff2",
};

// Headers the renderer must not pass on: the agent checks Origin against
// Host, and the token comes from the main process only.
const DROPPED_HEADERS = ["origin", "referer", "cookie", "host", "x-quill-token"];

function jsonError(status: number, error: string): Response {
  return new Response(JSON.stringify({ error }), { status, headers: { "Content-Type": "application/json" } });
}

// handleAppRequest serves quill://app/: the renderer bundle, and /api/* forwarded
// to `quill serve` with the session token.
export async function handleAppRequest(req: Request, deps: AppDeps): Promise<Response> {
  const url = new URL(req.url);
  if (url.host !== "app") return new Response("not found", { status: 404 });
  if (url.pathname === "/api" || url.pathname.startsWith("/api/")) return forward(req, url, deps);
  return serveStatic(url.pathname, deps.rendererDir);
}

async function forward(req: Request, url: URL, deps: AppDeps): Promise<Response> {
  const agent = deps.agent();
  if ("error" in agent) return jsonError(503, agent.error);
  const target = agent.url + url.pathname.slice("/api".length) + url.search;
  const headers = new Headers(req.headers);
  for (const h of DROPPED_HEADERS) headers.delete(h);
  headers.set("X-Quill-Token", agent.token);
  const hasBody = req.method !== "GET" && req.method !== "HEAD";
  try {
    const resp = await deps.fetch(target, {
      method: req.method,
      headers,
      body: hasBody ? req.body : undefined,
      signal: req.signal,
      ...(hasBody ? { duplex: "half" } : {}),
    } as RequestInit);
    return new Response(resp.body, { status: resp.status, statusText: resp.statusText, headers: resp.headers });
  } catch (e) {
    return jsonError(502, `The agent service at ${agent.url} did not answer (${(e as Error).message}). Is \`quill serve\` still running?`);
  }
}

async function serveStatic(pathname: string, rendererDir: string): Promise<Response> {
  const root = path.resolve(rendererDir);
  let rel: string;
  try {
    rel = decodeURIComponent(pathname);
  } catch {
    return new Response("bad path", { status: 400 });
  }
  const file = path.resolve(root, "." + path.posix.normalize("/" + rel));
  const inside = file === root || file.startsWith(root + path.sep);
  const candidate = inside && file !== root ? file : path.join(root, "index.html");
  let body: Buffer;
  let served = candidate;
  try {
    body = await fs.readFile(candidate);
  } catch {
    served = path.join(root, "index.html");
    body = await fs.readFile(served);
  }
  const type = CONTENT_TYPES[path.extname(served)] ?? "application/octet-stream";
  return new Response(new Uint8Array(body), { headers: { "Content-Type": type } });
}
