import { describe, expect, it } from "vitest";
import { CdpFilter, type CdpMessage } from "./cdpFilter.js";

const panel = { targetId: "PANEL", contextId: "CTX-PANEL" };
const targets = [
  { targetId: "BROWSER", type: "browser", title: "" },
  { targetId: "PANEL", type: "page", title: "Panel", browserContextId: "CTX-PANEL" },
  { targetId: "PANEL-TAB", type: "tab", title: "Panel", browserContextId: "CTX-PANEL" },
  { targetId: "GUI", type: "page", title: "quill", browserContextId: "CTX-GUI" },
  { targetId: "GUI-TAB", type: "tab", title: "quill", browserContextId: "CTX-GUI" },
];

// fakeChrome answers the filter's own calls like Chromium would and records
// everything the filter sends.
function setup() {
  const upstream: CdpMessage[] = [];
  const client: CdpMessage[] = [];
  let filter!: CdpFilter;
  filter = new CdpFilter(panel, {
    toUpstream: (m) => {
      upstream.push(m);
      if (m.id !== undefined && m.id >= CdpFilter.ownIdBase) {
        if (m.method === "Target.getTargets") filter.fromUpstream({ id: m.id, result: { targetInfos: targets } });
        else if (m.method === "Target.attachToTarget") filter.fromUpstream({ id: m.id, result: { sessionId: "OWN" } });
        else filter.fromUpstream({ id: m.id, result: {} });
      }
    },
    toClient: (m) => client.push(m),
  });
  const forwarded = () => upstream.filter((m) => m.id === undefined || m.id < CdpFilter.ownIdBase);
  return { filter, upstream, client, forwarded };
}

async function started() {
  const s = setup();
  await s.filter.start();
  return s;
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("CdpFilter client requests", () => {
  it("forwards requests for the panel target", async () => {
    const { filter, forwarded, client } = await started();
    filter.fromClient({ id: 1, method: "Target.attachToTarget", params: { targetId: "PANEL", flatten: true } });
    expect(forwarded()).toHaveLength(1);
    expect(client).toHaveLength(0);
  });

  it("rejects requests for a target outside the panel context", async () => {
    const { filter, forwarded, client } = await started();
    for (const [i, method] of ["Target.attachToTarget", "Target.activateTarget", "Target.closeTarget"].entries()) {
      filter.fromClient({ id: i + 1, method, params: { targetId: "GUI" } });
    }
    expect(forwarded()).toHaveLength(0);
    expect(client.map((m) => m.error?.message)).toEqual(Array(3).fill("target not allowed"));
  });

  it("rejects the browser target and unknown targets", async () => {
    const { filter, forwarded } = await started();
    filter.fromClient({ id: 1, method: "Target.attachToTarget", params: { targetId: "BROWSER" } });
    filter.fromClient({ id: 2, method: "Target.attachToTarget", params: { targetId: "NOPE" } });
    expect(forwarded()).toHaveLength(0);
  });

  it("rejects sessions it did not see attached", async () => {
    const { filter, forwarded, client } = await started();
    filter.fromClient({ id: 1, method: "Runtime.evaluate", params: { expression: "1" }, sessionId: "FORGED" });
    expect(forwarded()).toHaveLength(0);
    expect(client[0]).toMatchObject({ id: 1, sessionId: "FORGED", error: { message: "session not allowed" } });
  });

  it("allows a session once its attach to the panel succeeded", async () => {
    const { filter, forwarded } = await started();
    filter.fromClient({ id: 1, method: "Target.attachToTarget", params: { targetId: "PANEL", flatten: true } });
    filter.fromUpstream({ id: 1, result: { sessionId: "S1" } });
    filter.fromClient({ id: 2, method: "Runtime.evaluate", params: { expression: "1" }, sessionId: "S1" });
    expect(forwarded().map((m) => m.id)).toEqual([1, 2]);
  });

  it("rejects other browser contexts and context management", async () => {
    const { filter, forwarded, client } = await started();
    filter.fromClient({ id: 1, method: "Storage.getCookies", params: { browserContextId: "CTX-GUI" } });
    filter.fromClient({ id: 2, method: "Target.createBrowserContext", params: {} });
    filter.fromClient({ id: 3, method: "Target.disposeBrowserContext", params: { browserContextId: "CTX-PANEL" } });
    expect(forwarded()).toHaveLength(0);
    expect(client).toHaveLength(3);
  });

  it("denies commands that reach beyond the panel", async () => {
    const { filter, forwarded, client } = await started();
    const denied = ["Browser.close", "Browser.crash", "Target.attachToBrowserTarget", "Target.exposeDevToolsProtocol", "Target.openDevTools"];
    denied.forEach((method, i) => filter.fromClient({ id: i + 1, method, params: {} }));
    expect(forwarded()).toHaveLength(0);
    expect(client).toHaveLength(denied.length);
  });

  it("scopes default-context browser commands to the panel context", async () => {
    const { filter, forwarded } = await started();
    filter.fromClient({ id: 1, method: "Storage.getCookies", params: {} });
    filter.fromClient({ id: 2, method: "Browser.setDownloadBehavior", params: { behavior: "deny" } });
    expect(forwarded().map((m) => m.params?.browserContextId)).toEqual(["CTX-PANEL", "CTX-PANEL"]);
  });

  it("rewrites createTarget into a panel navigation", async () => {
    const { filter, upstream, client, forwarded } = await started();
    filter.fromClient({ id: 7, method: "Target.createTarget", params: { url: "http://localhost:3000/" } });
    await flush();
    expect(forwarded()).toHaveLength(0);
    expect(upstream.find((m) => m.method === "Page.navigate")).toMatchObject({ params: { url: "http://localhost:3000/" }, sessionId: "OWN" });
    expect(client).toEqual([{ id: 7, result: { targetId: "PANEL" } }]);
  });

  it("answers createTarget for about:blank with the panel", async () => {
    const { filter, upstream, client } = await started();
    filter.fromClient({ id: 7, method: "Target.createTarget", params: { url: "about:blank" } });
    expect(upstream.some((m) => m.method === "Page.navigate")).toBe(false);
    expect(client).toEqual([{ id: 7, result: { targetId: "PANEL" } }]);
  });

  it("queues client requests until the target map is seeded", async () => {
    const s = setup();
    s.filter.fromClient({ id: 1, method: "Target.attachToTarget", params: { targetId: "PANEL" } });
    expect(s.forwarded()).toHaveLength(0);
    await s.filter.start();
    expect(s.forwarded()).toHaveLength(1);
  });
});

describe("CdpFilter upstream messages", () => {
  it("filters getTargets results to the panel context", async () => {
    const { filter, client } = await started();
    filter.fromClient({ id: 1, method: "Target.getTargets", params: {} });
    filter.fromUpstream({ id: 1, result: { targetInfos: targets } });
    expect(client[0].result.targetInfos.map((t: { targetId: string }) => t.targetId)).toEqual(["BROWSER", "PANEL", "PANEL-TAB"]);
  });

  it("reports the panel context as the only and default context", async () => {
    const { filter, client } = await started();
    filter.fromClient({ id: 1, method: "Target.getBrowserContexts", params: {} });
    filter.fromUpstream({ id: 1, result: { browserContextIds: ["CTX-GUI", "CTX-PANEL"], defaultBrowserContextId: "CTX-GUI" } });
    expect(client[0].result).toEqual({ browserContextIds: ["CTX-PANEL"], defaultBrowserContextId: "CTX-PANEL" });
  });

  it("drops target events from other contexts", async () => {
    const { filter, client } = await started();
    filter.fromUpstream({ method: "Target.targetCreated", params: { targetInfo: targets[3] } });
    filter.fromUpstream({ method: "Target.targetInfoChanged", params: { targetInfo: targets[3] } });
    filter.fromUpstream({ method: "Target.targetDestroyed", params: { targetId: "GUI" } });
    filter.fromUpstream({ method: "Target.targetCreated", params: { targetInfo: targets[1] } });
    expect(client.map((m) => m.params.targetInfo?.targetId)).toEqual(["PANEL"]);
  });

  it("detaches auto-attached sessions it hides and keeps their messages out", async () => {
    const { filter, client, upstream } = await started();
    filter.fromUpstream({ method: "Target.attachedToTarget", params: { sessionId: "GUI-S", targetInfo: targets[3], waitingForDebugger: true } });
    filter.fromUpstream({ method: "Runtime.consoleAPICalled", params: {}, sessionId: "GUI-S" });
    expect(client).toHaveLength(0);
    expect(upstream.find((m) => m.method === "Target.detachFromTarget")).toMatchObject({ params: { sessionId: "GUI-S" } });
  });

  it("passes auto-attached panel sessions through", async () => {
    const { filter, client } = await started();
    filter.fromUpstream({ method: "Target.attachedToTarget", params: { sessionId: "P-S", targetInfo: targets[1] } });
    filter.fromUpstream({ method: "Page.loadEventFired", params: {}, sessionId: "P-S" });
    filter.fromClient({ id: 1, method: "Page.reload", params: {}, sessionId: "P-S" });
    expect(client.map((m) => m.method)).toEqual(["Target.attachedToTarget", "Page.loadEventFired"]);
  });

  it("forgets a session after it detaches", async () => {
    const { filter, forwarded, client } = await started();
    filter.fromUpstream({ method: "Target.attachedToTarget", params: { sessionId: "P-S", targetInfo: targets[1] } });
    filter.fromUpstream({ method: "Target.detachedFromTarget", params: { sessionId: "P-S" } });
    filter.fromClient({ id: 1, method: "Page.reload", params: {}, sessionId: "P-S" });
    expect(forwarded()).toHaveLength(0);
    expect(client.at(-1)?.error?.message).toBe("session not allowed");
  });

  it("never shows the GUI title to the client", async () => {
    const { filter, client } = await started();
    filter.fromClient({ id: 1, method: "Target.getTargets", params: {} });
    filter.fromUpstream({ id: 1, result: { targetInfos: targets } });
    filter.fromUpstream({ method: "Target.targetCreated", params: { targetInfo: targets[4] } });
    expect(JSON.stringify(client)).not.toContain("quill");
  });
});
