import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it, vi } from "vitest";
import { isAllowedPanelURL, readDevToolsPort, registerBrowser } from "./browserPolicy.js";

describe("isAllowedPanelURL", () => {
  it.each(["http://localhost:3000/", "http://127.0.0.1:5173/a?b", "https://localhost:8443/", "http://[::1]:8080/", "about:blank"])("allows %s", (url) => {
    expect(isAllowedPanelURL(url)).toBe(true);
  });

  it.each([
    "https://example.com/",
    "http://localhost.evil.com/",
    "http://10.0.0.1:3000/",
    "file:///etc/passwd",
    "quill://app/",
    "devtools://devtools/bundled/inspector.html",
    "chrome://settings",
    "javascript:alert(1)",
    "data:text/html,hi",
    "not a url",
  ])("blocks %s", (url) => {
    expect(isAllowedPanelURL(url)).toBe(false);
  });
});

describe("readDevToolsPort", () => {
  it("reads the port from DevToolsActivePort", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "quill-devtools-"));
    fs.writeFileSync(path.join(dir, "DevToolsActivePort"), "53211\n/devtools/browser/abc\n");
    expect(readDevToolsPort(dir)).toBe(53211);
  });

  it("returns null while the file is missing", () => {
    expect(readDevToolsPort(fs.mkdtempSync(path.join(os.tmpdir(), "quill-devtools-")))).toBeNull();
  });
});

describe("registerBrowser", () => {
  const agent = { url: "http://127.0.0.1:8123", token: "tok" };

  it("puts the endpoint with the token", async () => {
    const fetch = vi.fn(async () => new Response(null, { status: 204 }));
    await registerBrowser(agent, "http://127.0.0.1:9333", fetch);
    expect(fetch).toHaveBeenCalledWith("http://127.0.0.1:8123/browser", {
      method: "PUT",
      headers: { "Content-Type": "application/json", "X-Quill-Token": "tok" },
      body: JSON.stringify({ cdp_url: "http://127.0.0.1:9333" }),
    });
  });

  it("fails with the agent's reason", async () => {
    const fetch = vi.fn(async () => new Response("browser endpoint must be on loopback", { status: 400 }));
    await expect(registerBrowser(agent, "http://x:1", fetch)).rejects.toThrow("400: browser endpoint must be on loopback");
  });
});
