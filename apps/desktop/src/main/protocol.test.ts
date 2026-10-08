import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { AgentConfig } from "./agent";
import { handleAppRequest, type AppDeps } from "./protocol";

const rendererDir = fs.mkdtempSync(path.join(os.tmpdir(), "quill-renderer-"));
fs.writeFileSync(path.join(rendererDir, "index.html"), "<html>app</html>");
fs.mkdirSync(path.join(rendererDir, "assets"));
fs.writeFileSync(path.join(rendererDir, "assets", "app.js"), "console.log(1)");
fs.writeFileSync(path.join(rendererDir, "..", "secret.txt"), "secret");

type Seen = { url: string; init: RequestInit & { duplex?: string } };

function deps(agent: AgentConfig, respond?: (seen: Seen) => Response | Promise<Response>): AppDeps & { seen: Seen[] } {
  const seen: Seen[] = [];
  return {
    rendererDir,
    agent: () => agent,
    fetch: (async (url: string, init: RequestInit) => {
      const s = { url, init };
      seen.push(s);
      if (!respond) return new Response("{}");
      return respond(s);
    }) as unknown as typeof fetch,
    seen,
  };
}

const agent = { url: "http://127.0.0.1:8123", token: "tok" };

describe("handleAppRequest API forwarding", () => {
  it("strips /api, keeps the query and injects the token", async () => {
    const d = deps(agent);
    await handleAppRequest(new Request("quill://app/api/threads?cwd=%2Fp&limit=0", { headers: { "X-Quill-Token": "renderer" } }), d);
    expect(d.seen[0].url).toBe("http://127.0.0.1:8123/threads?cwd=%2Fp&limit=0");
    const h = new Headers(d.seen[0].init.headers);
    expect(h.get("X-Quill-Token")).toBe("tok");
  });

  it("does not forward the renderer origin or cookies", async () => {
    const d = deps(agent);
    await handleAppRequest(new Request("quill://app/api/info", { headers: { Origin: "quill://app", Cookie: "a=b" } }), d);
    const h = new Headers(d.seen[0].init.headers);
    expect(h.get("Origin")).toBeNull();
    expect(h.get("Cookie")).toBeNull();
  });

  it("forwards a POST body", async () => {
    const d = deps(agent, async (s) => new Response(await new Response(s.init.body).text()));
    const resp = await handleAppRequest(new Request("quill://app/api/runs", { method: "POST", body: '{"thread_id":"t"}' }), d);
    expect(d.seen[0].init.method).toBe("POST");
    expect(await resp.text()).toBe('{"thread_id":"t"}');
  });

  it("streams the agent response as it arrives", async () => {
    let push!: (s: string) => void;
    let close!: () => void;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        push = (s) => c.enqueue(new TextEncoder().encode(s));
        close = () => c.close();
      },
    });
    const d = deps(agent, () => new Response(body, { headers: { "Content-Type": "application/x-ndjson" } }));
    const resp = await handleAppRequest(new Request("quill://app/api/runs", { method: "POST", body: "{}" }), d);
    expect(resp.headers.get("Content-Type")).toBe("application/x-ndjson");
    const reader = resp.body!.getReader();
    push('{"kind":"delta"}\n');
    const first = await reader.read();
    expect(new TextDecoder().decode(first.value)).toBe('{"kind":"delta"}\n');
    close();
    expect((await reader.read()).done).toBe(true);
  });

  it("answers 503 with the reason when the agent is not running", async () => {
    const d = deps({ error: "The agent service is not running. Start it with `quill serve`." });
    const resp = await handleAppRequest(new Request("quill://app/api/info"), d);
    expect(resp.status).toBe(503);
    expect(((await resp.json()) as { error: string }).error).toContain("quill serve");
    expect(d.seen).toHaveLength(0);
  });

  it("answers 502 when the agent does not answer", async () => {
    const d = deps(agent, () => {
      throw new TypeError("fetch failed");
    });
    const resp = await handleAppRequest(new Request("quill://app/api/info"), d);
    expect(resp.status).toBe(502);
    expect(((await resp.json()) as { error: string }).error).toContain("http://127.0.0.1:8123");
  });
});

describe("handleAppRequest static files", () => {
  it("serves renderer files with a content type", async () => {
    const resp = await handleAppRequest(new Request("quill://app/assets/app.js"), deps(agent));
    expect(resp.status).toBe(200);
    expect(resp.headers.get("Content-Type")).toContain("javascript");
    expect(await resp.text()).toBe("console.log(1)");
  });

  it("falls back to index.html for app routes", async () => {
    for (const p of ["quill://app/", "quill://app/threads/abc"]) {
      const resp = await handleAppRequest(new Request(p), deps(agent));
      expect(await resp.text()).toBe("<html>app</html>");
    }
  });

  it("never serves files outside the renderer directory", async () => {
    const resp = await handleAppRequest(new Request("quill://app/%2e%2e/secret.txt"), deps(agent));
    expect(await resp.text()).not.toBe("secret");
  });

  it("refuses other hosts", async () => {
    const resp = await handleAppRequest(new Request("quill://evil/api/info"), deps(agent));
    expect(resp.status).toBe(404);
  });
});
