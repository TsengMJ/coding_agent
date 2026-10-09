import fs from "node:fs";
import path from "node:path";
import type { AgentConfig } from "./agent.js";

// The desktop app talks either to a `quill serve` on this machine (token
// from ~/.quill/agent.json) or to a team server, signed in with GitHub's
// device flow. Team server sessions are encrypted with Electron's
// safeStorage, whose key lives in the macOS Keychain (or the platform's
// secret store), and only ever leave the main process in the
// Authorization header of requests to the server that issued them.

export type Connection = { mode: "local" } | { mode: "remote"; url: string };

export type ConnectionState =
  | { mode: "local" }
  | { mode: "remote"; url: string; signedIn: boolean; user?: string };

// Crypter is the part of Electron's safeStorage the token store needs.
export interface Crypter {
  isEncryptionAvailable(): boolean;
  encryptString(plain: string): Buffer;
  decryptString(encrypted: Buffer): string;
}

export interface DeviceCode {
  device_code: string;
  user_code: string;
  verification_uri: string;
  expires_in: number;
  interval: number;
}

export interface Session {
  token: string;
  user: { id: string; name?: string };
}

const LOOPBACK_HOSTS = new Set(["localhost", "127.0.0.1", "[::1]"]);

// normalizeServerURL accepts https:// URLs, and http:// only on loopback
// (a server under development): sessions must not cross the network in
// clear text.
export function normalizeServerURL(raw: string): string | { error: string } {
  let u: URL;
  try {
    u = new URL(raw.trim());
  } catch {
    return { error: `"${raw}" is not a URL; use https://quill.example.com` };
  }
  if (u.protocol !== "https:" && !(u.protocol === "http:" && LOOPBACK_HOSTS.has(u.hostname))) {
    return { error: "The server URL must start with https:// (http:// is allowed only for localhost)" };
  }
  if (u.username || u.password || u.search || u.hash) {
    return { error: "The server URL cannot carry a user name, query or fragment" };
  }
  return u.origin + u.pathname.replace(/\/+$/, "");
}

// TokenStore keeps one encrypted session per server URL.
export class TokenStore {
  constructor(
    private readonly file: string,
    private readonly crypt: Crypter,
  ) {}

  private read(): Record<string, string> {
    try {
      const raw = JSON.parse(fs.readFileSync(this.file, "utf8")) as unknown;
      return raw && typeof raw === "object" ? (raw as Record<string, string>) : {};
    } catch {
      return {};
    }
  }

  private write(all: Record<string, string>): void {
    fs.mkdirSync(path.dirname(this.file), { recursive: true });
    fs.writeFileSync(this.file, JSON.stringify(all), { mode: 0o600 });
  }

  get(url: string): string | undefined {
    const enc = this.read()[url];
    if (!enc || !this.crypt.isEncryptionAvailable()) return undefined;
    try {
      return this.crypt.decryptString(Buffer.from(enc, "base64"));
    } catch {
      return undefined;
    }
  }

  set(url: string, token: string): void {
    if (!this.crypt.isEncryptionAvailable()) {
      throw new Error("The system keychain is unavailable, so the session cannot be stored safely.");
    }
    this.write({ ...this.read(), [url]: this.crypt.encryptString(token).toString("base64") });
  }

  delete(url: string): void {
    const all = this.read();
    if (!(url in all)) return;
    delete all[url];
    this.write(all);
  }
}

async function errorText(resp: Response): Promise<string> {
  const body = await resp.text().catch(() => "");
  try {
    return (JSON.parse(body) as { error?: string }).error ?? body;
  } catch {
    return body || `HTTP ${resp.status}`;
  }
}

export async function startDeviceLogin(url: string, fetchFn: typeof fetch): Promise<DeviceCode> {
  const resp = await fetchFn(`${url}/auth/device`, { method: "POST" });
  if (!resp.ok) throw new Error(`The server could not start the GitHub login: ${await errorText(resp)}`);
  return (await resp.json()) as DeviceCode;
}

export type PollDeps = {
  fetch: typeof fetch;
  sleep: (ms: number) => Promise<void>;
  signal?: AbortSignal;
};

// waitForLogin polls the server until the user enters the code on GitHub.
// The server answers 428 while the login is pending (its message says
// "slow_down" when GitHub wants slower polling), 410 once the code
// expired and 403 when GitHub or the server's allow-list refused it.
export async function waitForLogin(url: string, code: DeviceCode, deps: PollDeps): Promise<Session> {
  let interval = Math.max(1, code.interval || 5) * 1000;
  const deadline = Date.now() + Math.max(1, code.expires_in || 900) * 1000;
  for (;;) {
    await deps.sleep(interval);
    if (deps.signal?.aborted) throw new Error("Sign-in cancelled.");
    if (Date.now() > deadline) throw new Error("The GitHub code expired; sign in again.");
    const resp = await deps.fetch(`${url}/auth/token`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ device_code: code.device_code }),
      signal: deps.signal,
    });
    if (resp.ok) return (await resp.json()) as Session;
    const msg = await errorText(resp);
    switch (resp.status) {
      case 428:
        if (msg.includes("slow")) interval += 5000;
        continue;
      case 410:
        throw new Error("The GitHub code expired; sign in again.");
      case 403:
        throw new Error(`Sign-in refused: ${msg}`);
      default:
        throw new Error(`Sign-in failed (HTTP ${resp.status}): ${msg}`);
    }
  }
}

export type ConnectionDeps = {
  // dir holds connection.json and the encrypted tokens.json.
  dir: string;
  crypt: Crypter;
  fetch: typeof fetch;
  sleep?: (ms: number) => Promise<void>;
  // local finds the `quill serve` on this machine.
  local: () => AgentConfig;
};

// Connections is the main process's view of where the agent runs.
export class Connections {
  private readonly tokens: TokenStore;
  private readonly file: string;
  private conn: Connection;
  private user = "";
  private pending: { url: string; ctrl: AbortController; done: Promise<ConnectionState> } | undefined;

  constructor(private readonly deps: ConnectionDeps) {
    this.tokens = new TokenStore(path.join(deps.dir, "tokens.json"), deps.crypt);
    this.file = path.join(deps.dir, "connection.json");
    this.conn = this.readConnection();
  }

  private readConnection(): Connection {
    try {
      const raw = JSON.parse(fs.readFileSync(this.file, "utf8")) as { mode?: string; url?: string };
      if (raw.mode === "remote" && typeof raw.url === "string") {
        const url = normalizeServerURL(raw.url);
        if (typeof url === "string") return { mode: "remote", url };
      }
    } catch {
      // no saved connection: local
    }
    return { mode: "local" };
  }

  private save(conn: Connection): void {
    this.conn = conn;
    fs.mkdirSync(this.deps.dir, { recursive: true });
    fs.writeFileSync(this.file, JSON.stringify(conn));
  }

  state(): ConnectionState {
    if (this.conn.mode === "local") return { mode: "local" };
    const signedIn = this.tokens.get(this.conn.url) !== undefined;
    return { mode: "remote", url: this.conn.url, signedIn, ...(signedIn && this.user ? { user: this.user } : {}) };
  }

  // agent is where /api requests go, read per request.
  agent(): AgentConfig {
    if (this.conn.mode === "local") return this.deps.local();
    const token = this.tokens.get(this.conn.url);
    if (!token) return { error: `Sign in to ${this.conn.url} to use the team server.` };
    return { url: this.conn.url, token, remote: true };
  }

  useLocal(): ConnectionState {
    this.pending?.ctrl.abort();
    this.save({ mode: "local" });
    return this.state();
  }

  // login starts the GitHub device flow for url and returns the code to
  // show; wait() resolves when the user finished it.
  async login(rawURL: string): Promise<DeviceCode> {
    const url = normalizeServerURL(rawURL);
    if (typeof url !== "string") throw new Error(url.error);
    this.pending?.ctrl.abort();
    const code = await startDeviceLogin(url, this.deps.fetch);
    const ctrl = new AbortController();
    const sleep = this.deps.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
    const done = waitForLogin(url, code, { fetch: this.deps.fetch, sleep, signal: ctrl.signal }).then((s) => {
      this.tokens.set(url, s.token);
      this.user = s.user.name || s.user.id;
      this.save({ mode: "remote", url });
      return this.state();
    });
    done.catch(() => undefined);
    this.pending = { url, ctrl, done };
    return code;
  }

  async wait(): Promise<ConnectionState> {
    if (!this.pending) return this.state();
    try {
      return await this.pending.done;
    } finally {
      this.pending = undefined;
    }
  }

  async logout(): Promise<ConnectionState> {
    if (this.conn.mode === "remote") {
      const token = this.tokens.get(this.conn.url);
      if (token) {
        await this.deps
          .fetch(`${this.conn.url}/auth/logout`, { method: "POST", headers: { Authorization: `Bearer ${token}` } })
          .catch(() => undefined);
      }
      this.tokens.delete(this.conn.url);
      this.user = "";
    }
    return this.state();
  }

  // unauthorized forgets a session the server no longer accepts.
  unauthorized(url: string): void {
    if (this.conn.mode === "remote" && this.conn.url === url) {
      this.tokens.delete(url);
      this.user = "";
    }
  }
}
