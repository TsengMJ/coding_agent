import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { VirtuosoHandle } from "react-virtuoso";
import { Api, ApiError } from "./api";
import type { Decision, Info, Message, SlashCommand, ThreadInfo } from "./types";
import { helpText, resolveCommand } from "./model/commands";
import { applyEvent, emptyView, endRun, fromHistory, startRun, type ThreadView } from "./model/state";
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

const userMessage = (text: string): Message => ({ role: "user", content: [{ type: "text", text }] });

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
  const [view, setView] = useState<ThreadView>(emptyView);
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
  const abortRef = useRef<AbortController | null>(null);
  const listRef = useRef<VirtuosoHandle | null>(null);
  const searchRef = useRef<HTMLInputElement | null>(null);
  const composerRef = useRef<HTMLTextAreaElement | null>(null);

  const refreshThreads = useCallback(
    async (cwd: string) => {
      try {
        setThreads(await api.threads(allDirs ? "" : cwd));
      } catch (e) {
        setNotice(`Could not list threads: ${(e as Error).message}`);
      }
    },
    [api, allDirs],
  );

  const loadThread = useCallback(
    async (id: string) => {
      abortRef.current?.abort();
      setThreadId(id);
      setExpanded(new Set());
      setSelected("");
      setInspect(null);
      setView(emptyView());
      try {
        const [ts, trace] = await Promise.all([api.thread(id), api.trace(id)]);
        setView(fromHistory(ts, trace));
      } catch (e) {
        setView({ ...emptyView(), error: `Could not load thread ${id}: ${(e as Error).message}` });
      }
    },
    [api],
  );

  useEffect(() => {
    api
      .info()
      .then((i) => {
        setInfo(i);
        setRunCwd(i.cwd);
        setAutoApprove(i.auto_approve);
        void loadThread(i.thread_id);
      })
      .catch((e: Error) => {
        setFatal(
          e instanceof ApiError && e.status === 403
            ? "This page needs the session token. Open the URL that dcode printed in the terminal."
            : `Cannot reach dcode: ${e.message}`,
        );
      });
  }, [api, loadThread]);

  useEffect(() => {
    if (info) void refreshThreads(info.cwd);
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

  const run = useCallback(
    async (req: { messages?: Message[]; resume?: { decisions: Decision[] } }, userText: string) => {
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      setView((v) => startRun(v, userText));
      let error = "";
      try {
        for await (const ev of api.run({ thread_id: threadId, cwd: runCwd, auto_approve: autoApprove, ...req }, ctrl.signal)) {
          if (ev.event === "done") break;
          if (ev.event === "error") error = ev.error ?? "run failed";
          setView((v) => applyEvent(v, ev));
        }
      } catch (e) {
        if (!ctrl.signal.aborted) error = (e as Error).message;
      } finally {
        if (ctrl.signal.aborted && !error) error = "Stopped.";
        setView((v) => endRun(v, error));
        abortRef.current = null;
        void refreshThreads(info?.cwd ?? "");
      }
    },
    [api, threadId, runCwd, autoApprove, refreshThreads, info],
  );

  const newThread = useCallback(() => {
    if (!info) return;
    setRunCwd(info.cwd);
    void loadThread(newThreadID());
  }, [info, loadThread]);

  const send = useCallback(
    async (text: string) => {
      const cmd = resolveCommand(text, commands);
      if (cmd.kind === "text") {
        void run({ messages: [userMessage(text)] }, text);
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
          else void loadThread(cmd.args);
          return;
        case "/yolo":
          setAutoApprove(!autoApprove);
          setNotice(autoApprove ? "Auto-approve off." : "Auto-approve on: tool calls run without asking.");
          return;
        case "/help":
          setNotice(helpText(commands));
          return;
        case "/quit":
          setNotice("Close this tab to leave; the dcode process keeps running until you stop it in the terminal.");
          return;
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
    [api, threadId, run, newThread, loadThread, commands, autoApprove],
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
    }),
    [api, threadId, expanded, selected, inspect, query],
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
        <h2>dcode</h2>
        <div>{fatal}</div>
      </div>
    );
  }
  if (!info) return <div className="empty-state">Connecting to dcode…</div>;

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

  return (
    <UIContext.Provider value={ui}>
      <div className={`app ${inspect ? "with-drawer" : ""}`}>
        <Sidebar
          threads={threads}
          current={threadId}
          allDirs={allDirs}
          onAllDirs={setAllDirs}
          onPick={(t) => {
            setRunCwd(t.cwd || info.cwd);
            void loadThread(t.thread_id);
          }}
          onNew={newThread}
          version={info.version}
        />
        <main className="main">
          <header className="header">
            <span className="pill" title="model">
              {info.model}
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
            {view.running && <span className="pill running">running</span>}
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
          <Timeline ref={listRef} turns={shownTurns} live={live} threadId={threadId} />
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
            onSend={(t) => void send(t)}
            onStop={() => abortRef.current?.abort()}
          />
        </main>
        {inspect && (
          <LLMInspector api={api} threadId={threadId} seq={inspect.seq} label={inspect.label} onClose={() => setInspect(null)} />
        )}
      </div>
    </UIContext.Provider>
  );
}
