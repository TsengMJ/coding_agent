import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { Connections, normalizeServerURL, TokenStore, waitForLogin, type Crypter, type DeviceCode } from "./connection";

// fakeCrypt "encrypts" by reversing, so tests can see nothing is stored in clear.
const fakeCrypt = (available = true): Crypter => ({
  isEncryptionAvailable: () => available,
  encryptString: (s) => Buffer.from([...s].reverse().join("")),
  decryptString: (b) => [...b.toString()].reverse().join(""),
});

const tmp = () => fs.mkdtempSync(path.join(os.tmpdir(), "quill-conn-"));

type Call = { url: string; init?: RequestInit };

// server answers each request with the next scripted response for its path.
function server(script: Record<string, Array<() => Response>>) {
  const calls: Call[] = [];
  const fetchFn = (async (url: string, init?: RequestInit) => {
    calls.push({ url, init });
    const next = script[new URL(url).pathname]?.shift();
    if (!next) throw new Error(`unexpected request ${url}`);
    return next();
  }) as unknown as typeof fetch;
  return { calls, fetch: fetchFn };
}

const json = (status: number, body: unknown) => () => new Response(JSON.stringify(body), { status });
const code: DeviceCode = { device_code: "dc", user_code: "ABCD-1234", verification_uri: "https://github.com/login/device", expires_in: 900, interval: 5 };
const noSleep = async () => undefined;

describe("normalizeServerURL", () => {
  it("accepts https and drops the trailing slash", () => {
    expect(normalizeServerURL(" https://quill.example.com/ ")).toBe("https://quill.example.com");
    expect(normalizeServerURL("https://example.com/quill/")).toBe("https://example.com/quill");
  });

  it("allows http only on loopback", () => {
    expect(normalizeServerURL("http://localhost:8123")).toBe("http://localhost:8123");
    expect(normalizeServerURL("http://quill.example.com")).toHaveProperty("error");
  });

  it("refuses credentials, queries and non-URLs", () => {
    expect(normalizeServerURL("https://u:p@quill.example.com")).toHaveProperty("error");
    expect(normalizeServerURL("https://quill.example.com/?x=1")).toHaveProperty("error");
    expect(normalizeServerURL("quill.example.com")).toHaveProperty("error");
  });
});

describe("TokenStore", () => {
  it("stores tokens encrypted, per server", () => {
    const file = path.join(tmp(), "tokens.json");
    const s = new TokenStore(file, fakeCrypt());
    s.set("https://a", "secret-a");
    s.set("https://b", "secret-b");
    expect(s.get("https://a")).toBe("secret-a");
    expect(fs.readFileSync(file, "utf8")).not.toContain("secret");
    s.delete("https://a");
    expect(s.get("https://a")).toBeUndefined();
    expect(s.get("https://b")).toBe("secret-b");
  });

  it("refuses to store a token without the keychain", () => {
    const s = new TokenStore(path.join(tmp(), "tokens.json"), fakeCrypt(false));
    expect(() => s.set("https://a", "t")).toThrow(/keychain/);
  });
});

describe("waitForLogin", () => {
  it("polls while pending, slows down when asked and returns the session", async () => {
    const waits: number[] = [];
    const srv = server({
      "/auth/token": [
        json(428, { error: "authorization_pending" }),
        json(428, { error: "slow_down" }),
        json(200, { token: "sess", user: { id: "alice" } }),
      ],
    });
    const s = await waitForLogin("https://q", code, { fetch: srv.fetch, sleep: async (ms) => void waits.push(ms) });
    expect(s.token).toBe("sess");
    expect(waits).toEqual([5000, 5000, 10000]);
    expect(JSON.parse(String(srv.calls[0].init?.body))).toEqual({ device_code: "dc" });
  });

  it("stops on an expired or refused code", async () => {
    const expired = server({ "/auth/token": [json(410, { error: "expired_token" })] });
    await expect(waitForLogin("https://q", code, { fetch: expired.fetch, sleep: noSleep })).rejects.toThrow(/expired/);
    const refused = server({ "/auth/token": [json(403, { error: "bob is not on the allow list" })] });
    await expect(waitForLogin("https://q", code, { fetch: refused.fetch, sleep: noSleep })).rejects.toThrow(/allow list/);
  });
});

describe("Connections", () => {
  const local = () => ({ url: "http://127.0.0.1:8123", token: "local" });

  it("uses the local quill serve until the user signs in to a team server", async () => {
    const dir = tmp();
    const srv = server({
      "/auth/device": [json(200, code)],
      "/auth/token": [json(200, { token: "sess", user: { id: "alice", name: "Alice" } })],
      "/auth/logout": [json(204, {})],
    });
    const c = new Connections({ dir, crypt: fakeCrypt(), fetch: srv.fetch, sleep: noSleep, local });
    expect(c.agent()).toEqual(local());

    const shown = await c.login("https://quill.example.com/");
    expect(shown.user_code).toBe("ABCD-1234");
    expect(await c.wait()).toEqual({ mode: "remote", url: "https://quill.example.com", signedIn: true, user: "Alice" });
    expect(c.agent()).toEqual({ url: "https://quill.example.com", token: "sess", remote: true });

    const reopened = new Connections({ dir, crypt: fakeCrypt(), fetch: srv.fetch, local });
    expect(reopened.agent()).toEqual({ url: "https://quill.example.com", token: "sess", remote: true });

    await reopened.logout();
    expect(new Headers(srv.calls.at(-1)?.init?.headers).get("Authorization")).toBe("Bearer sess");
    expect(reopened.agent()).toHaveProperty("error");
    expect(reopened.useLocal()).toEqual({ mode: "local" });
    expect(reopened.agent()).toEqual(local());
  });

  it("forgets a session the server refused", async () => {
    const srv = server({ "/auth/device": [json(200, code)], "/auth/token": [json(200, { token: "sess", user: { id: "alice" } })] });
    const c = new Connections({ dir: tmp(), crypt: fakeCrypt(), fetch: srv.fetch, sleep: noSleep, local });
    await c.login("https://q.example.com");
    await c.wait();
    c.unauthorized("https://q.example.com");
    expect(c.state()).toEqual({ mode: "remote", url: "https://q.example.com", signedIn: false });
  });

  it("refuses a plain http server", async () => {
    const c = new Connections({ dir: tmp(), crypt: fakeCrypt(), fetch: server({}).fetch, local });
    await expect(c.login("http://quill.example.com")).rejects.toThrow(/https/);
  });
});
