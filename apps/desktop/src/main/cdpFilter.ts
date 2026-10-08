// Filters one CDP connection so the client (a browser MCP server) only sees
// and controls the browser panel. Targets are matched by browser context:
// the panel has its own session partition, so its page, its "tab" wrapper
// and anything it opens share one context that the quill UI is not part of.

// CDP params and results are arbitrary JSON.
type Json = any;

export interface CdpMessage {
  id?: number;
  method?: string;
  params?: Json;
  sessionId?: string;
  result?: Json;
  error?: { code: number; message: string };
}

export interface PanelRef {
  targetId: string;
  contextId: string;
}

interface TargetInfo {
  targetId: string;
  type: string;
  browserContextId?: string;
}

// Commands that act on the whole browser or hand out unfiltered access.
const DENIED = new Set([
  "Browser.close",
  "Browser.crash",
  "Browser.crashGpuProcess",
  "Target.attachToBrowserTarget",
  "Target.exposeDevToolsProtocol",
  "Target.openDevTools",
  "Target.setRemoteLocations",
  "Target.createBrowserContext",
  "Target.disposeBrowserContext",
]);

// Browser-level commands that default to the default context (the quill UI)
// when browserContextId is omitted.
const CONTEXT_SCOPED = new Set([
  "Storage.getCookies",
  "Storage.setCookies",
  "Storage.clearCookies",
  "Browser.setDownloadBehavior",
  "Browser.cancelDownload",
  "Browser.grantPermissions",
  "Browser.resetPermissions",
]);

export class CdpFilter {
  // Ids of the filter's own requests; client ids stay far below.
  static readonly ownIdBase = 1_000_000_000;

  private readonly targetContext = new Map<string, string | undefined>();
  private readonly sessions = new Set<string>();
  private readonly pendingMethods = new Map<number, string>();
  private readonly own = new Map<number, (m: CdpMessage) => void>();
  private nextOwnId = CdpFilter.ownIdBase;
  private queue: CdpMessage[] | null = [];

  constructor(
    private readonly panel: PanelRef,
    private readonly io: { toUpstream: (m: CdpMessage) => void; toClient: (m: CdpMessage) => void },
  ) {}

  // start seeds the target map; client requests wait until it is done.
  async start(): Promise<void> {
    const res = await this.call("Target.getTargets", { filter: [{}] });
    for (const info of (res.result?.targetInfos ?? []) as TargetInfo[]) this.learn(info);
    const queued = this.queue ?? [];
    this.queue = null;
    for (const m of queued) this.fromClient(m);
  }

  fromClient(msg: CdpMessage): void {
    if (this.queue) {
      this.queue.push(msg);
      return;
    }
    const params = msg.params ?? {};
    const deny = (message: string) => this.io.toClient({ id: msg.id, sessionId: msg.sessionId, error: { code: -32000, message } });
    if (msg.sessionId && !this.sessions.has(msg.sessionId)) return deny("session not allowed");
    const method = msg.method ?? "";
    if (DENIED.has(method)) return deny(`${method} is not allowed in the quill browser panel`);
    if (method === "Target.createTarget") return this.createTarget(msg);
    if (params.targetId && !this.allowedTarget(params.targetId)) return deny("target not allowed");
    if (params.browserContextId && params.browserContextId !== this.panel.contextId) return deny("browser context not allowed");
    if (!msg.sessionId && CONTEXT_SCOPED.has(method) && !params.browserContextId) {
      msg = { ...msg, params: { ...params, browserContextId: this.panel.contextId } };
    }
    if (msg.id !== undefined) this.pendingMethods.set(msg.id, method);
    this.io.toUpstream(msg);
  }

  fromUpstream(msg: CdpMessage): void {
    if (msg.id !== undefined && this.own.has(msg.id)) {
      this.own.get(msg.id)!(msg);
      this.own.delete(msg.id);
      return;
    }
    if (msg.sessionId && !this.sessions.has(msg.sessionId)) return;
    const params = msg.params ?? {};
    const info: TargetInfo | undefined = params.targetInfo;
    if (info) {
      this.learn(info);
      if (!this.visible(info)) {
        if (msg.method === "Target.attachedToTarget") void this.call("Target.detachFromTarget", { sessionId: params.sessionId });
        return;
      }
    }
    switch (msg.method) {
      case "Target.attachedToTarget":
        this.sessions.add(params.sessionId);
        break;
      case "Target.detachedFromTarget":
        if (!this.sessions.delete(params.sessionId)) return;
        break;
      case "Target.targetDestroyed":
      case "Target.targetCrashed":
        if (!this.allowedTarget(params.targetId)) return;
        break;
    }
    if (msg.id !== undefined) {
      const method = this.pendingMethods.get(msg.id);
      this.pendingMethods.delete(msg.id);
      if (msg.result) msg = { ...msg, result: this.filterResult(method, msg.result) };
    }
    this.io.toClient(msg);
  }

  private filterResult(method: string | undefined, result: Json): Json {
    switch (method) {
      case "Target.getTargets": {
        const infos = (result.targetInfos ?? []) as TargetInfo[];
        infos.forEach((i) => this.learn(i));
        return { ...result, targetInfos: infos.filter((i) => this.visible(i)) };
      }
      case "Target.getBrowserContexts":
        return { ...result, browserContextIds: [this.panel.contextId], defaultBrowserContextId: this.panel.contextId };
      case "Target.attachToTarget":
        if (result.sessionId) this.sessions.add(result.sessionId);
        return result;
      default:
        return result;
    }
  }

  // Electron cannot open tabs, so a new tab becomes a navigation of the panel.
  private createTarget(msg: CdpMessage): void {
    const url: string | undefined = msg.params?.url;
    const reply = () => this.io.toClient({ id: msg.id, result: { targetId: this.panel.targetId } });
    if (!url || url === "about:blank") return reply();
    void (async () => {
      const attached = await this.call("Target.attachToTarget", { targetId: this.panel.targetId, flatten: true });
      const sessionId: string | undefined = attached.result?.sessionId;
      if (!sessionId) {
        this.io.toClient({ id: msg.id, error: { code: -32000, message: attached.error?.message ?? "cannot attach to the panel" } });
        return;
      }
      const nav = await this.call("Page.navigate", { url }, sessionId);
      await this.call("Target.detachFromTarget", { sessionId });
      if (nav.error) this.io.toClient({ id: msg.id, error: nav.error });
      else reply();
    })();
  }

  private call(method: string, params: Json, sessionId?: string): Promise<CdpMessage> {
    const id = this.nextOwnId++;
    return new Promise((resolve) => {
      this.own.set(id, resolve);
      this.io.toUpstream(sessionId ? { id, method, params, sessionId } : { id, method, params });
    });
  }

  private learn(info: TargetInfo): void {
    this.targetContext.set(info.targetId, info.browserContextId);
  }

  private allowedTarget(targetId: string): boolean {
    return this.targetContext.get(targetId) === this.panel.contextId;
  }

  private visible(info: TargetInfo): boolean {
    return info.type === "browser" || info.browserContextId === this.panel.contextId;
  }
}
