import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from "react";
import type { VirtuosoHandle } from "react-virtuoso";
import { Api, ApiError } from "./api";
import type { Decision, Info, Message, RunEvent, RunMode, SlashCommand, ThreadInfo, ThreadUsage } from "./types";
import { helpText, parseModelArgs, resolveCommand } from "./model/commands";
import { contextText, costText, tokensText } from "./model/usage";
import { applyEvent, emptyView, endRun, fromHistory, showBuild, startRun, type ThreadView } from "./model/state";
import { BUILD_MESSAGE } from "./model/plan";
import { pendingText, userContent, type ImageAttachment } from "./model/images";
import { PlanBanner } from "./components/Plan";
import { buildTimeline, type LLMStep, type Turn } from "./model/timeline";
import { matchTurn, type Filter } from "./model/filter";
import { formatTokens } from "./model/messages";
import { UIContext, type UI } from "./ui";
import { Timeline, type LiveState } from "./components/Timeline";
import { Sidebar } from "./components/Sidebar";
import { Composer } from "./components/Composer";
import { ApprovalCard } from "./components/Approval";
import { LLMInspector } from "./components/LLMInspector";
import { CopyButton } from "./components/common";

function newThreadID(): string {
  const b = new Uint8Array(6);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}

// The ID lets a reattached page tell this message from its replay.
const userMessage = (text: string, images: ImageAttachment[] = []): Message => ({
  id: `user-${newThreadID()}`,
  role: "user",
  content: userContent(text, images),
});

const DRAWER_STORAGE_KEY = "quill.inspectorWidth";
const SIDEBAR_WIDTH = 260;
const RESIZE_HANDLE_WIDTH = 8;
const DRAWER_MIN_WIDTH = 420;
const MAIN_MIN_WIDTH = 480;
const DRAWER_OVERLAY_WIDTH = SIDEBAR_WIDTH + RESIZE_HANDLE_WIDTH + DRAWER_MIN_WIDTH + MAIN_MIN_WIDTH;

type Draft = { text: string; literal: boolean };
type HistoryCursor = { checkpointSeq: number; hasMore: boolean; nextBefore: number; loading: boolean; error: string };

function savedDrawerWidth(): number | null {
  try {
    const value = Number(localStorage.getItem(DRAWER_STORAGE_KEY));
    return Number.isFinite(value) && value >= DRAWER_MIN_WIDTH ? value : null;
  } catch {
    return null;
  }
}

function clampDrawerWidth(width: number, viewportWidth: number): number {
  return Math.max(DRAWER_MIN_WIDTH, Math.min(width, viewportWidth - SIDEBAR_WIDTH - RESIZE_HANDLE_WIDTH - MAIN_MIN_WIDTH));
}

function persistDrawerWidth(width: number): void {
  try {
    localStorage.setItem(DRAWER_STORAGE_KEY, String(width));
  } catch {
    // The drawer remains resizable when browser storage is unavailable.
  }
}

const FILTERS: { id: Filter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "edits", label: "Edits" },
  { id: "commands", label: "Commands" },
  { id: "errors", label: "Errors" },
  { id: "llm", label: "LLM calls only" },
];

function allToolKeys(turns: Turn[]): string[] {
  const keys: string[] = [];
  const walk = (steps: LLMStep[]) => {
    for (const s of steps) {
      for (const t of s.tools) {
        keys.push(t.key);
        if (t.sub) walk(t.sub.steps);
      }
    }
  };
  for (const t of turns) walk(t.steps);
  return keys;
}

export function App({ api }: { api: Api }) {
  const [info, setInfo] = useState<Info | null>(null);
  const [fatal, setFatal] = useState("");
  const [threadId, setThreadId] = useState("");
  const [runCwd, setRunCwd] = useState("");
  // A running stream must belong to its thread even after the user switches
  // the visible conversation. Keeping views keyed by ID also lets several
  // threads run at once in one browser page.
  const [views, setViews] = useState<Record<string, ThreadView>>({});
  const [threads, setThreads] = useState<ThreadInfo[]>([]);
  const [allDirs, setAllDirs] = useState(false);
  const [autoApprove, setAutoApprove] = useState(false);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [selected, setSelected] = useState("");
  const [inspect, setInspect] = useState<{ seq: number; label: string } | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [query, setQuery] = useState("");
  const [notice, setNotice] = useState("");
  const [commands, setCommands] = useState<SlashCommand[]>([]);
  // modelOverride is sent with every run (quill -model or /model); threadModel
  // is the model the current thread last ran with.
  const [modelOverride, setModelOverride] = useState("");
  const [threadModels, setThreadModels] = useState<Record<string, string>>({});
  const [usage, setUsage] = useState<ThreadUsage | null>(null);
  const [mode, setMode] = useState<RunMode>("agent");
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [historyCursors, setHistoryCursors] = useState<Record<string, HistoryCursor>>({});
  const [diffsLoading, setDiffsLoading] = useState<Record<string, boolean>>({});
  const [replaceCandidate, setReplaceCandidate] = useState<string | null>(null);
  const [viewportWidth, setViewportWidth] = useState(() => window.innerWidth);
  const [drawerWidth, setDrawerWidth] = useState(() => savedDrawerWidth() ?? Math.round(window.innerWidth * 0.42));
  const drawerWidthRef = useRef(drawerWidth);
  const abortRefs = useRef<Record<string, AbortController | undefined>>({});
  // threads whose run the user asked to stop
  const stopRefs = useRef(new Set<string>());
  const listRef = useRef<VirtuosoHandle | null>(null);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const composerRef = useRef<HTMLTextAreaElement | null>(null);
  const currentThreadRef = useRef("");
  const loadSeqRef = useRef(0);
  const listSeqRef = useRef(0);

  const setViewFor = useCallback((id: string, update: ThreadView | ((previous: ThreadView) => ThreadView)) => {
    setViews((previous) => {
      const current = previous[id] ?? emptyView();
      const next = typeof update === "function" ? update(current) : update;
      return { ...previous, [id]: next };
    });
  }, []);
  const view = views[threadId] ?? emptyView();
  const threadModel = threadModels[threadId] ?? "";

  useEffect(() => {
    const onResize = () => setViewportWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const setInspectorWidth = useCallback((width: number) => {
    const next = clampDrawerWidth(width, window.innerWidth);
    drawerWidthRef.current = next;
    setDrawerWidth(next);
  }, []);

  const finishInspectorResize = useCallback(() => persistDrawerWidth(drawerWidthRef.current), []);

  const startInspectorResize = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    e.preventDefault();
  }, []);

  const moveInspectorResize = useCallback((e: ReactPointerEvent<HTMLDivElement>) => {
    if (!e.currentTarget.hasPointerCapture(e.pointerId)) return;
    setInspectorWidth(window.innerWidth - e.clientX - RESIZE_HANDLE_WIDTH / 2);
  }, [setInspectorWidth]);

  const draft = drafts[threadId] ?? { text: "", literal: false };
  const updateDraft = useCallback((text: string, literal: boolean) => {
    setDrafts((prev) => ({ ...prev, [threadId]: { text, literal: !!text && literal } }));
    setReplaceCandidate(null);
  }, [threadId]);

  const fillFromHistory = useCallback((text: string) => {
    setDrafts((prev) => ({ ...prev, [threadId]: { text, literal: true } }));
    setReplaceCandidate(null);
    requestAnimationFrame(() => {
      composerRef.current?.scrollIntoView({ block: "nearest" });
      composerRef.current?.focus();
    });
  }, [threadId]);

  const reuseUserMessage = useCallback((text: string) => {
    if (view.running || view.interrupts.length > 0 || !text.trim()) return;
    if (draft.text.trim() && draft.text !== text) {
      setReplaceCandidate(text);
      composerRef.current?.scrollIntoView({ block: "nearest" });
      return;
    }
    fillFromHistory(text);
  }, [draft.text, fillFromHistory, view.running, view.interrupts.length]);

  const refreshThreads = useCallback(
    async (cwd: string) => {
      const seq = ++listSeqRef.current;
      try {
        const list = await api.threads(allDirs ? "" : cwd);
        if (seq === listSeqRef.current) setThreads(list);
      } catch (e) {
        if (seq === listSeqRef.current) setNotice(`Could not list threads: ${(e as Error).message}`);
      }
    },
    [api, allDirs],
  );

  const refreshUsage = useCallback(
    async (id: string) => {
      try {
        const next = await api.usage(id);
        if (id === currentThreadRef.current) setUsage(next);
      } catch {
        if (id === currentThreadRef.current) setUsage(null);
      }
    },
    [api],
  );

  // refreshPlan picks up the plan a run created or started building.
  const refreshPlan = useCallback(
    async (id: string) => {
      try {
        const ts = await api.history(id);
        if (ts) setViewFor(id, (v) => ({ ...v, plan: ts.plan ?? null }));
      } catch {
        // keep the last known plan
      }
    },
    [api, setViewFor],
  );

  // follow folds a run's events into its thread's view until the stream
  // ends; ctrl must already be registered in abortRefs.
  const follow = useCallback(
    async (id: string, ctrl: AbortController, events: () => Promise<AsyncIterable<RunEvent>>) => {
      let error = "";
      try {
        for await (const ev of await events()) {
          if (ev.event === "done") break;
          if (ev.event === "error") error = ev.error ?? "run failed";
          setViewFor(id, (v) => applyEvent(v, ev));
        }
      } catch (e) {
        if (!ctrl.signal.aborted) error = (e as Error).message;
      } finally {
        if (stopRefs.current.delete(id) || (ctrl.signal.aborted && !error)) error = "Stopped.";
        setViewFor(id, (v) => endRun(v, error));
        delete abortRefs.current[id];
        void refreshThreads(info?.cwd ?? "");
        void refreshUsage(id);
        void refreshPlan(id);
      }
    },
    [info, refreshThreads, refreshUsage, refreshPlan, setViewFor],
  );

  // attach shows a run that is still going on the server, e.g. after this
  // page was reloaded.
  const attach = useCallback(
    async (id: string) => {
      if (abortRefs.current[id]) return;
      const ctrl = new AbortController();
      abortRefs.current[id] = ctrl;
      let events: AsyncGenerator<RunEvent> | null = null;
      try {
        events = await api.events(id, ctrl.signal);
      } catch {
        // the history already shows the saved state
      }
      if (!events) {
        delete abortRefs.current[id];
        return;
      }
      const stream = events;
      setViewFor(id, (v) => startRun(v, ""));
      await follow(id, ctrl, async () => stream);
    },
    [api, follow, setViewFor],
  );
  const attachRef = useRef(attach);
  attachRef.current = attach;

  const loadThread = useCallback(
    async (id: string) => {
      const seq = ++loadSeqRef.current;
      currentThreadRef.current = id;
      setThreadId(id);
      setExpanded(new Set());
      setSelected("");
      setInspect(null);
      setReplaceCandidate(null);
      // This page is streaming the thread's run; its view is newer than
      // any saved checkpoint.
      if (abortRefs.current[id]) return;
      setViewFor(id, (v) => ({ ...v, error: "" }));
      setHistoryCursors((pages) => ({ ...pages, [id]: { checkpointSeq: 0, hasMore: false, nextBefore: 0, loading: true, error: "" } }));
      setThreadModels((models) => ({ ...models, [id]: "" }));
      setUsage(null);
      try {
        const [ts, trace] = await Promise.all([api.history(id), api.traceSummary(id)]);
        if (seq !== loadSeqRef.current || abortRefs.current[id]) return;
        setThreadModels((models) => ({ ...models, [id]: ts?.model ?? "" }));
        setViewFor(id, fromHistory(ts, trace));
        setHistoryCursors((pages) => ({ ...pages, [id]: {
          checkpointSeq: ts?.checkpoint_seq ?? 0, hasMore: ts?.has_more ?? false,
          nextBefore: ts?.next_before ?? 0, loading: false, error: "",
        } }));
        if (ts) void attachRef.current(id);
      } catch (e) {
        if (seq === loadSeqRef.current) {
          setViewFor(id, { ...emptyView(), error: `Could not load thread ${id}: ${(e as Error).message}` });
          setHistoryCursors((pages) => ({ ...pages, [id]: { checkpointSeq: 0, hasMore: false, nextBefore: 0, loading: false, error: (e as Error).message } }));
        }
      }
  },
    [api, setViewFor],
  );

  const loadOlder = useCallback(async () => {
    const cursor = historyCursors[threadId];
    if (!cursor?.hasMore || cursor.loading || !cursor.nextBefore) return;
    setHistoryCursors((pages) => ({ ...pages, [threadId]: { ...cursor, loading: true, error: "" } }));
    try {
      const page = await api.history(threadId, cursor.nextBefore);
      if (!page || page.checkpoint_seq !== cursor.checkpointSeq) {
        void loadThread(threadId);
        return;
      }
      setViewFor(threadId, (v) => ({ ...v, messages: [...page.messages, ...v.messages] }));
      setHistoryCursors((pages) => ({ ...pages, [threadId]: {
        checkpointSeq: page.checkpoint_seq, hasMore: page.has_more,
        nextBefore: page.next_before ?? 0, loading: false, error: "",
      } }));
    } catch (e) {
      setHistoryCursors((pages) => ({ ...pages, [threadId]: { ...cursor, loading: false, error: (e as Error).message } }));
    }
  }, [api, historyCursors, loadThread, setViewFor, threadId]);

  const loadDiffs = useCallback(() => {
    if (diffsLoading[threadId]) return;
    setDiffsLoading((loading) => ({ ...loading, [threadId]: true }));
    void api.trace(threadId).then((trace) => {
      setViewFor(threadId, (v) => ({ ...v, diffs: trace.diffs }));
    }).catch((e: Error) => {
      setNotice(`Could not load file diffs: ${e.message}`);
    }).finally(() => {
      setDiffsLoading((loading) => ({ ...loading, [threadId]: false }));
    });
  }, [api, diffsLoading, setViewFor, threadId]);

  useEffect(() => {
    api
      .info()
      .then((i) => {
        setInfo(i);
        setRunCwd(i.cwd);
        setAutoApprove(i.auto_approve);
        setModelOverride(i.model_override ?? "");
        void loadThread(i.thread_id);
      })
      .catch((e: Error) => {
        setFatal(
          e instanceof ApiError && e.status === 403
            ? "This page needs the session token. Open the URL that quill printed in the terminal."
            : `Cannot reach quill: ${e.message}`,
        );
      });
  }, [api, loadThread]);

  useEffect(() => {
    if (info) void refreshThreads(info.cwd);
  }, [info, refreshThreads]);

  useEffect(() => {
    if (!info) return;
    const onFocus = () => void refreshThreads(info.cwd);
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [info, refreshThreads]);

  // Running is process-local state. Poll the inexpensive thread catalogue
  // while this page is visible so a reload or another GUI page still shows
  // every active thread.
  useEffect(() => {
    if (!info) return;
    const tick = () => {
      if (document.visibilityState === "visible") void refreshThreads(info.cwd);
    };
    const id = window.setInterval(tick, 3000);
    return () => window.clearInterval(id);
  }, [info, refreshThreads]);

  useEffect(() => {
    api
      .commands()
      .then(setCommands)
      .catch(() => setCommands([]));
  }, [api]);

  const timeline = useMemo(
    () => buildTimeline(view),
    // streamText and pendingUser change on every token; the timeline does not depend on them
    [view.messages, view.sub, view.llm, view.diffs, view.interrupts, view.running],
  );
  const visibleTurns = useMemo(
    () => timeline.turns.filter((t) => matchTurn(t, filter, query)),
    [timeline.turns, filter, query],
  );
  const shownTurns = useMemo(
    () => (filter === "llm" ? visibleTurns.map((t) => ({ ...t, steps: t.steps.map((s) => ({ ...s, tools: [], batch: undefined })) })) : visibleTurns),
    [visibleTurns, filter],
  );
  const noSearchResults = timeline.turns.length > 0 && shownTurns.length === 0 && (filter !== "all" || !!query.trim());

  const run = useCallback(
    async (
      req: { messages?: Message[]; resume?: { decisions: Decision[] }; mode?: RunMode; build_plan?: boolean },
      userText: string,
    ) => {
      const runThreadID = threadId;
      const runCwdForThread = runCwd;
      const runAutoApprove = autoApprove;
      const runModel = modelOverride || undefined;
      const runMode = mode;
      const ctrl = new AbortController();
      abortRefs.current[runThreadID] = ctrl;
      setViewFor(runThreadID, (v) => startRun(v, userText));
      void refreshThreads(info?.cwd ?? "");
      const body = { thread_id: runThreadID, cwd: runCwdForThread, auto_approve: runAutoApprove, model: runModel, mode: runMode, ...req };
      await follow(runThreadID, ctrl, async () => api.run(body, ctrl.signal));
      if (modelOverride) setThreadModels((models) => ({ ...models, [runThreadID]: modelOverride }));
    },
    [api, threadId, runCwd, autoApprove, modelOverride, mode, refreshThreads, follow, info, setViewFor],
  );

  // stop cancels the run on the server and lets its stream end, so the
  // messages saved while stopping still show; the abort is the fallback.
  const stop = useCallback((id: string) => {
    const ctrl = abortRefs.current[id];
    if (!ctrl) return;
    stopRefs.current.add(id);
    api.cancel(id).catch(() => ctrl.abort());
  }, [api]);

  const build = useCallback(() => {
    setMode("agent");
    void run({ messages: [userMessage(BUILD_MESSAGE)], build_plan: true, mode: "agent" }, BUILD_MESSAGE);
  }, [run]);

  const switchMode = useCallback((m: RunMode) => {
    setMode(m);
    setNotice(m === "plan" ? "Plan mode on: quill researches read-only and writes a plan for you to review; Build implements it." : "Plan mode off.");
  }, []);

  const newThread = useCallback(() => {
    if (!info) return;
    setRunCwd(info.cwd);
    void loadThread(newThreadID());
  }, [info, loadThread]);

  const renameThread = useCallback(async (id: string, title: string): Promise<boolean> => {
    try {
      const updated = await api.renameThread(id, title);
      setThreads((old) => old.map((t) => t.thread_id === id ? { ...t, ...updated } : t));
      return true;
    } catch (e) {
      setNotice(`Could not rename thread: ${(e as Error).message}`);
      return false;
    }
  }, [api]);

  // resumeThread switches to an existing thread and adopts its remembered model.
  const resumeThread = useCallback(
    (id: string) => {
      setModelOverride("");
      void loadThread(id);
    },
    [loadThread],
  );

  const currentModel = modelOverride || threadModel || info?.model || "";

  const send = useCallback(
    async (text: string, literal = false, images: ImageAttachment[] = []) => {
      // A message with images is never a slash command, so the images are not lost.
      const cmd = literal || images.length ? null : resolveCommand(text, commands);
      if (!cmd || cmd.kind === "text") {
        void run({ messages: [userMessage(text, images)] }, pendingText(text, images.length));
        return;
      }
      if (cmd.kind === "unknown") {
        setNotice(`Unknown command ${cmd.name}${cmd.suggestion ? ` — did you mean ${cmd.suggestion}?` : ""} (try /help)`);
        return;
      }
      switch (cmd.name) {
        case "/new":
          return newThread();
        case "/thread":
          setNotice(`Thread ${threadId}`);
          return;
        case "/threads":
          setNotice("Threads are listed in the sidebar.");
          return;
        case "/resume":
          if (!cmd.args) setNotice("Usage: /resume <thread id>");
          else resumeThread(cmd.args);
          return;
        case "/model": {
          const m = parseModelArgs(cmd.args);
          if ("error" in m) {
            setNotice(m.error);
            return;
          }
          if (!m.spec && !m.saveDefault) {
            setNotice(`Model ${currentModel || "(server default)"}`);
            return;
          }
          const spec = m.spec || currentModel;
          if (m.spec) setModelOverride(m.spec);
          if (!m.saveDefault) {
            setNotice(`Model ${spec} (from the next message)`);
            return;
          }
          try {
            await api.saveDefaultModel(spec);
            setNotice(`Model ${spec}; saved as the default model.`);
          } catch (e) {
            setNotice(`Could not save the default model: ${(e as Error).message}`);
          }
          return;
        }
        case "/yolo":
          setAutoApprove(!autoApprove);
          setNotice(autoApprove ? "Auto-approve off." : "Auto-approve on: tool calls run without asking.");
          return;
        case "/plan":
          switchMode(mode === "plan" ? "agent" : "plan");
          return;
        case "/build":
          return build();
        case "/help":
          setNotice(helpText(commands));
          return;
        case "/quit":
          setNotice("Close this tab to leave; the quill process keeps running until you stop it in the terminal.");
          return;
        case "/tokens":
        case "/context":
        case "/cost": {
          const format = { "/tokens": tokensText, "/context": contextText, "/cost": costText }[cmd.name];
          try {
            const u = await api.usage(threadId);
            setUsage(u);
            setNotice(u ? format(u) : "No messages in this thread yet.");
          } catch (e) {
            setNotice(`Could not load usage: ${(e as Error).message}`);
          }
          return;
        }
        case "/compact":
          try {
            const r = await api.compact(threadId);
            setNotice(`Compacted: older history summarized${r.file_path ? ` (full history saved to ${r.file_path})` : ""}.`);
            await loadThread(threadId);
          } catch (e) {
            setNotice(`Compact failed: ${(e as Error).message}`);
          }
          return;
        default:
          setNotice(`${cmd.name} is not available in the GUI.`);
      }
    },
    [api, threadId, run, newThread, loadThread, resumeThread, commands, autoApprove, currentModel, mode, switchMode, build],
  );

  const ui: UI = useMemo(
    () => ({
      api,
      threadId,
      expanded,
      toggle: (key) =>
        setExpanded((s) => {
          const n = new Set(s);
          if (n.has(key)) n.delete(key);
          else n.add(key);
          return n;
        }),
      selected,
      select: setSelected,
      inspect: (seq, label) => setInspect({ seq, label }),
      inspecting: inspect?.seq,
      query,
      loadDiffs,
      diffsLoading: !!diffsLoading[threadId],
    }),
    [api, threadId, expanded, selected, inspect, query, loadDiffs, diffsLoading],
  );

  // keyboard navigation over the rendered rows
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement;
      const typing = target.tagName === "TEXTAREA" || target.tagName === "INPUT";
      if (e.key === "Escape") {
        if (typing) target.blur();
        else if (inspect) setInspect(null);
        return;
      }
      if (typing || e.metaKey || e.ctrlKey || e.altKey) return;
      const rows = [...document.querySelectorAll<HTMLElement>(".timeline [data-key]")];
      const at = rows.findIndex((r) => r.dataset.key === selected);
      const move = (d: number) => {
        const next = rows[Math.max(0, Math.min(rows.length - 1, at < 0 ? (d > 0 ? 0 : rows.length - 1) : at + d))];
        if (next) {
          setSelected(next.dataset.key!);
          next.scrollIntoView({ block: "nearest" });
        }
      };
      switch (e.key) {
        case "j":
          move(1);
          break;
        case "k":
          move(-1);
          break;
        case "Enter":
          if (at >= 0) rows[at].click();
          break;
        case "e": {
          const keys = allToolKeys(visibleTurns);
          setExpanded((s) => (keys.every((k) => s.has(k)) ? new Set() : new Set(keys)));
          break;
        }
        case "/":
          e.preventDefault();
          searchRef.current?.focus();
          break;
        case "i":
          composerRef.current?.focus();
          e.preventDefault();
          break;
        case "G":
          listRef.current?.scrollToIndex({ index: "LAST", align: "end" });
          break;
        default:
          return;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selected, inspect, visibleTurns]);

  if (fatal) {
    return (
      <div className="empty-state">
        <h2>quill</h2>
        <div>{fatal}</div>
      </div>
    );
  }
  if (!info) return <div className="empty-state">Connecting to quill…</div>;

  const lastStep = timeline.turns.at(-1)?.steps.at(-1);
  const waitingOnTools = lastStep?.tools.some((t) => t.status === "running") ?? false;
  const live: LiveState = {
    pendingUser: view.pendingUser,
    streamText: view.streamText,
    running: view.running,
    thinking: view.running && !view.streamText && !waitingOnTools,
    nextLabel: `main · LLM #${timeline.llmCount + 1}`,
    error: view.error,
  };
  const blocked = view.interrupts.length > 0;
  const overlayDrawer = viewportWidth < DRAWER_OVERLAY_WIDTH;
  const inspectorWidth = clampDrawerWidth(drawerWidth, viewportWidth);

  return (
    <UIContext.Provider value={ui}>
      <div className={`app ${inspect ? "with-drawer" : ""}`} style={{ "--drawer-width": `${inspectorWidth}px` } as CSSProperties}>
        <Sidebar
          threads={threads}
          current={threadId}
          allDirs={allDirs}
          onAllDirs={setAllDirs}
          onPick={(t) => {
            setRunCwd(t.cwd || info.cwd);
            resumeThread(t.thread_id);
          }}
          onNew={newThread}
          onRename={renameThread}
          version={info.version}
        />
        <main className="main">
          <header className="header">
            <span className="pill model-pill" title={currentModel}>
              {currentModel}
            </span>
            <span className="cwd" title={runCwd}>
              {runCwd}
            </span>
            <span className="pill mono" title="thread id">
              {threadId} <CopyButton text={threadId} label="⧉" />
            </span>
            <span className="spacer" />
            <span className="pill" title="LLM calls and tokens in this thread">
              {timeline.llmCount} LLM calls · {formatTokens(timeline.tokens)} tokens
            </span>
            {usage?.context_limit ? (
              <span className="pill" title={tokensText(usage)}>
                context {Math.round(usage.context_percent ?? 0)}%
              </span>
            ) : null}
            {view.running && <span className="pill running">running</span>}
            {mode === "plan" && (
              <button className="pill plan" onClick={() => switchMode("agent")} title="Plan mode is read-only; click to leave it">
                plan mode
              </button>
            )}
            <button
              className={`pill ${autoApprove ? "on" : ""}`}
              onClick={() => setAutoApprove(!autoApprove)}
              title="When on, file edits and commands run without asking"
            >
              auto-approve {autoApprove ? "on" : "off"}
            </button>
          </header>
          <div className="toolbar">
            {FILTERS.map((f) => (
              <button key={f.id} className={`chip ${filter === f.id ? "active" : ""}`} onClick={() => setFilter(f.id)}>
                {f.label}
              </button>
            ))}
            <input
              ref={searchRef}
              className="search"
              placeholder="Search messages, commands, outputs  ( / )"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            {query && <button className="clear-search" type="button" onClick={() => setQuery("")} aria-label="Clear search">×</button>}
            {(filter !== "all" || query) && (
              <span className="muted" style={{ fontSize: 12 }}>
                {visibleTurns.length} of {timeline.turns.length} turns
              </span>
            )}
            <span style={{ flex: 1 }} />
            <button
              className="chip"
              onClick={() => {
                const keys = allToolKeys(visibleTurns);
                setExpanded((s) => (keys.every((k) => s.has(k)) ? new Set() : new Set(keys)));
              }}
            >
              expand / collapse all
            </button>
          </div>
          {notice && (
            <div className="note" style={{ margin: "6px 16px", whiteSpace: "pre-line" }} onClick={() => setNotice("")}>
              {notice} <span className="muted">(click to dismiss)</span>
            </div>
          )}
          {noSearchResults ? (
            <div className="timeline empty-state"><h2>No matching messages</h2><div>Try another search or clear the current filters.</div><button className="btn" type="button" onClick={() => { setQuery(""); setFilter("all"); }}>Clear filters</button></div>
          ) : <Timeline
            ref={listRef}
            turns={shownTurns}
            live={live}
            threadId={threadId}
            onReuseUserMessage={reuseUserMessage}
            reuseDisabled={view.running || blocked}
            hasMore={historyCursors[threadId]?.hasMore}
            loadingOlder={historyCursors[threadId]?.loading}
            olderError={historyCursors[threadId]?.error}
            onLoadOlder={() => void loadOlder()}
            loading={historyCursors[threadId]?.loading && view.messages.length === 0}
          />}
          {showBuild(view) && view.plan && <PlanBanner plan={view.plan} busy={view.running} onBuild={build} />}
          {blocked && !view.running && (
            <ApprovalCard
              interrupts={view.interrupts}
              busy={view.running}
              onDecide={(decisions) => void run({ resume: { decisions } }, "")}
            />
          )}
          <Composer
            ref={composerRef}
            running={view.running}
            blocked={blocked}
            commands={commands}
            mode={mode}
            text={draft.text}
            literal={draft.literal}
            replacePending={replaceCandidate !== null}
            onTextChange={updateDraft}
            onReplaceConfirm={() => { if (replaceCandidate !== null) fillFromHistory(replaceCandidate); }}
            onReplaceCancel={() => setReplaceCandidate(null)}
            onMode={switchMode}
            onSend={(t, literal, images) => void send(t, literal, images)}
            onStop={() => stop(threadId)}
          />
        </main>
        {inspect && (
          <>
            {!overlayDrawer && (
              <div
                className="drawer-resize"
                role="separator"
                aria-label="Resize model inspector"
                aria-orientation="vertical"
                aria-controls="llm-inspector"
                aria-valuemin={DRAWER_MIN_WIDTH}
                aria-valuemax={viewportWidth - SIDEBAR_WIDTH - RESIZE_HANDLE_WIDTH - MAIN_MIN_WIDTH}
                aria-valuenow={inspectorWidth}
                tabIndex={0}
                onPointerDown={startInspectorResize}
                onPointerMove={moveInspectorResize}
                onPointerUp={(e) => { if (e.currentTarget.hasPointerCapture(e.pointerId)) { e.currentTarget.releasePointerCapture(e.pointerId); finishInspectorResize(); } }}
                onPointerCancel={finishInspectorResize}
                onKeyDown={(e) => {
                  const step = e.shiftKey ? 64 : 16;
                  if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
                    e.preventDefault();
                    const next = clampDrawerWidth(inspectorWidth + (e.key === "ArrowLeft" ? step : -step), window.innerWidth);
                    setInspectorWidth(next);
                    persistDrawerWidth(next);
                  } else if (e.key === "Home" || e.key === "End") {
                    e.preventDefault();
                    const next = e.key === "Home" ? DRAWER_MIN_WIDTH : window.innerWidth - SIDEBAR_WIDTH - RESIZE_HANDLE_WIDTH - MAIN_MIN_WIDTH;
                    setInspectorWidth(next);
                    persistDrawerWidth(next);
                  }
                }}
              />
            )}
            {overlayDrawer && <button className="drawer-backdrop" aria-label="Close model inspector" onClick={() => setInspect(null)} />}
            <LLMInspector api={api} threadId={threadId} seq={inspect.seq} label={inspect.label} onClose={() => setInspect(null)} />
          </>
        )}
      </div>
    </UIContext.Provider>
  );
}
