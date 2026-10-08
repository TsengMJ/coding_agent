import type {
  Decision,
  Info,
  HistoryPage,
  LLMCallDetail,
  Message,
  RunEvent,
  RunMode,
  SlashCommand,
  ThreadInfo,
  ThreadState,
  ThreadUsage,
  Trace,
} from "./types";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

// Api calls the agent through the quill:// protocol; the Electron main
// process adds the session token, so this page never holds it.
export class Api {
  private async request(path: string, init: RequestInit = {}): Promise<Response> {
    const resp = await fetch("api" + path, {
      ...init,
      headers: { ...(init.headers ?? {}), "Content-Type": "application/json" },
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

  commands(): Promise<SlashCommand[]> {
    return this.json("/commands");
  }

  threads(cwd: string, limit = 0): Promise<ThreadInfo[]> {
    const q = new URLSearchParams({ limit: String(limit) });
    if (cwd) q.set("cwd", cwd);
    return this.json(`/threads?${q}`);
  }

  renameThread(id: string, title: string): Promise<ThreadInfo> {
    return this.json(`/threads/${encodeURIComponent(id)}`, {
      method: "PATCH",
      body: JSON.stringify({ title }),
    });
  }

  async thread(id: string): Promise<ThreadState | null> {
    try {
      return await this.json(`/threads/${encodeURIComponent(id)}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  }

  async history(id: string, before = 0, limit = 20): Promise<HistoryPage | null> {
    try {
      const q = new URLSearchParams({ limit: String(limit) });
      if (before) q.set("before", String(before));
      return await this.json(`/threads/${encodeURIComponent(id)}/history?${q}`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  }

  async usage(id: string): Promise<ThreadUsage | null> {
    try {
      return await this.json(`/threads/${encodeURIComponent(id)}/usage`);
    } catch (e) {
      if (e instanceof ApiError && e.status === 404) return null;
      throw e;
    }
  }

  trace(id: string): Promise<Trace> {
    return this.json(`/threads/${encodeURIComponent(id)}/trace`);
  }

  traceSummary(id: string): Promise<Trace> {
    return this.json(`/threads/${encodeURIComponent(id)}/trace/summary`);
  }

  llmCall(id: string, seq: number): Promise<LLMCallDetail> {
    return this.json(`/threads/${encodeURIComponent(id)}/trace/llm/${seq}`);
  }

  compact(id: string): Promise<{ summary: string; file_path?: string }> {
    return this.json(`/threads/${encodeURIComponent(id)}/compact`, { method: "POST" });
  }

  async saveDefaultModel(spec: string): Promise<void> {
    await this.request("/default-model", { method: "PUT", body: JSON.stringify({ model: spec }) });
  }

  // run streams the events of one run. The run keeps going on the server
  // when signal aborts (a reload can reattach with events); cancel stops it.
  async *run(
    req: {
      thread_id: string;
      cwd: string;
      messages?: Message[];
      resume?: { decisions: Decision[] };
      auto_approve: boolean;
      model?: string;
      mode?: RunMode;
      build_plan?: boolean;
    },
    signal: AbortSignal,
  ): AsyncGenerator<RunEvent> {
    const resp = await this.request("/runs?detach=1", {
      method: "POST",
      body: JSON.stringify({ ...req, interactive: true }),
      signal,
    });
    yield* readNDJSON(resp.body!);
  }

  // events reattaches to the thread's running run, replaying it from its
  // first event; null when the thread is idle.
  async events(id: string, signal: AbortSignal): Promise<AsyncGenerator<RunEvent> | null> {
    const resp = await this.request(`/threads/${encodeURIComponent(id)}/events`, { signal });
    if (resp.status === 204 || !resp.body) return null;
    return readNDJSON(resp.body);
  }

  async cancel(id: string): Promise<void> {
    try {
      await this.request(`/threads/${encodeURIComponent(id)}/cancel`, { method: "POST" });
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 404)) throw e;
    }
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
