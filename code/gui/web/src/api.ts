import type {
  Decision,
  Info,
  LLMCallDetail,
  Message,
  RunEvent,
  ThreadInfo,
  ThreadState,
  Trace,
} from "./types";

const TOKEN_KEY = "dcode-token";

// takeToken moves the token from the URL fragment dcode opened into
// session storage, so it is not left in the address bar or history.
export function takeToken(): string {
  const m = /(?:^|[#&])token=([^&]+)/.exec(window.location.hash);
  if (m) {
    sessionStorage.setItem(TOKEN_KEY, m[1]);
    history.replaceState(null, "", window.location.pathname + window.location.search);
  }
  return sessionStorage.getItem(TOKEN_KEY) ?? "";
}

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export class Api {
  private token: string;
  constructor(token: string) {
    this.token = token;
  }

  private async request(path: string, init: RequestInit = {}): Promise<Response> {
    const resp = await fetch("api" + path, {
      ...init,
      headers: { ...(init.headers ?? {}), "X-Dcode-Token": this.token, "Content-Type": "application/json" },
    });
    if (!resp.ok) {
      let msg = `HTTP ${resp.status}`;
      try {
        const body = await resp.text();
        try {
          msg = (JSON.parse(body) as { error?: string }).error ?? body;
        } catch {
          msg = body || msg;
        }
      } catch {
        // keep the status text
      }
      throw new ApiError(resp.status, msg.trim());
    }
    return resp;
  }

  private async json<T>(path: string, init?: RequestInit): Promise<T> {
    return (await this.request(path, init)).json() as Promise<T>;
  }

  info(): Promise<Info> {
    return this.json("/info");
  }

  threads(cwd: string, limit = 50): Promise<ThreadInfo[]> {
    const q = new URLSearchParams({ limit: String(limit) });
    if (cwd) q.set("cwd", cwd);
    return this.json(`/threads?${q}`);
  }

  async thread(id: string): Promise<ThreadState | null> {
    try {
      return await this.json(`/threads/${encodeURIComponent(id)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  }

  trace(id: string): Promise<Trace> {
    return this.json(`/threads/${encodeURIComponent(id)}/trace`);
  }

  llmCall(id: string, seq: number): Promise<LLMCallDetail> {
    return this.json(`/threads/${encodeURIComponent(id)}/trace/llm/${seq}`);
  }

  compact(id: string): Promise<{ summary: string; file_path?: string }> {
    return this.json(`/threads/${encodeURIComponent(id)}/compact`, { method: "POST" });
  }

  // run streams the events of one run; aborting signal cancels it on the
  // server too.
  async *run(
    req: {
      thread_id: string;
      cwd: string;
      messages?: Message[];
      resume?: { decisions: Decision[] };
      auto_approve: boolean;
    },
    signal: AbortSignal,
  ): AsyncGenerator<RunEvent> {
    const resp = await this.request("/runs", {
      method: "POST",
      body: JSON.stringify({ ...req, interactive: true }),
      signal,
    });
    yield* readNDJSON(resp.body!);
  }
}

// readNDJSON yields one parsed object per line of a streamed body.
export async function* readNDJSON(body: ReadableStream<Uint8Array>): AsyncGenerator<RunEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buf = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let nl: number;
    while ((nl = buf.indexOf("\n")) >= 0) {
      const line = buf.slice(0, nl).trim();
      buf = buf.slice(nl + 1);
      if (line) yield JSON.parse(line) as RunEvent;
    }
  }
  const rest = buf.trim();
  if (rest) yield JSON.parse(rest) as RunEvent;
}
