import { forwardRef, useEffect, useState } from "react";
import { Virtuoso, type VirtuosoHandle } from "react-virtuoso";
import type { LLMStep, SubRun, ToolStep, Turn } from "../model/timeline";
import { groupFiles } from "../model/timeline";
import { formatDuration, textOf } from "../model/messages";
import { summarize } from "../model/tools";
import { useUI } from "../ui";
import { DiffStat, Highlighted, Markdown } from "./common";
import { DiffView } from "./DiffView";
import { ToolDetail } from "./ToolDetail";
import { category, firstLine, llmMeta, resultSummary } from "./labels";
import { failedCommand } from "../model/filter";

export interface LiveState {
  pendingUser: string;
  streamText: string;
  running: boolean;
  thinking: boolean;
  nextLabel: string;
  error: string;
}

type Item =
  | { kind: "header"; turn: Turn }
  | { kind: "step"; turn: Turn; index: number }
  | { kind: "files"; turn: Turn }
  | { kind: "live" };

// Timeline is the scrollable history: one virtualized item per turn, so
// long threads stay fast, plus a live item for what is streaming now. It
// follows new output only while scrolled to the bottom.
export const Timeline = forwardRef<VirtuosoHandle, {
  turns: Turn[];
  live: LiveState;
  threadId: string;
  onReuseUserMessage: (text: string) => void;
  reuseDisabled: boolean;
  hasMore?: boolean;
  loadingOlder?: boolean;
  olderError?: string;
  onLoadOlder?: () => void;
  loading?: boolean;
}>(
  function Timeline({ turns, live, threadId, onReuseUserMessage, reuseDisabled, hasMore, loadingOlder, olderError, onLoadOlder, loading }, ref) {
    // A user turn can contain dozens of model calls and tool rows. Keep each
    // step virtualized independently so one long turn cannot defeat Virtuoso.
    const items: Item[] = [];
    for (const turn of turns) {
      if (turn.user || turn.notes.length) items.push({ kind: "header", turn });
      turn.steps.forEach((_, index) => items.push({ kind: "step", turn, index }));
      if (turn.files.length) items.push({ kind: "files", turn });
    }
    items.push({ kind: "live" });
    const [atBottom, setAtBottom] = useState(true);
    const handle = ref as React.RefObject<VirtuosoHandle | null>;
    useEffect(() => {
      if (atBottom) handle.current?.scrollToIndex({ index: "LAST", align: "end" });
    }, [live.streamText, live.pendingUser, live.running, atBottom, handle]);
    if (loading && turns.length === 0) {
      return <div className="timeline empty-state" role="status"><h2>Loading conversation…</h2><div>Reading the most recent messages.</div></div>;
    }
    if (turns.length === 0 && !live.pendingUser && !live.running) {
      return (
        <div className="timeline empty-state">
          <h2>Start a conversation</h2>
          <div>Ask quill to read, search, edit or run things in this directory.</div>
          <div className="hint">Every step can be clicked to see the exact command, output and model input.</div>
        </div>
      );
    }
    return (
      <>
      <Virtuoso
        key={threadId}
        ref={ref}
        className="timeline"
        data={items}
        initialTopMostItemIndex={items.length - 1}
        startReached={() => { if (hasMore && !loadingOlder) onLoadOlder?.(); }}
        followOutput={(bottom) => (bottom ? "auto" : false)}
        atBottomStateChange={setAtBottom}
        atBottomThreshold={80}
        increaseViewportBy={{ top: 800, bottom: 800 }}
        computeItemKey={(_, it) => it.kind === "header" ? `${it.turn.key}-header` : it.kind === "step" ? it.turn.steps[it.index].key : it.kind === "files" ? `${it.turn.key}-files` : "live"}
        itemContent={(_, it) => {
          if (it.kind === "header") return <TurnHeader turn={it.turn} onReuseUserMessage={onReuseUserMessage} reuseDisabled={reuseDisabled} />;
          if (it.kind === "step") return <div className="turn"><StepView step={it.turn.steps[it.index]} /></div>;
          if (it.kind === "files") return <FilesChanged turn={it.turn} />;
          return <LiveView live={live} />;
        }}
      />
      {(loadingOlder || olderError) && (
        <div className="history-load-state" role="status">
          {loadingOlder ? "Loading earlier messages…" : <>Could not load earlier messages. <button type="button" onClick={onLoadOlder}>Retry</button></>}
        </div>
      )}
      </>
    );
  },
);

function TurnHeader({ turn, onReuseUserMessage, reuseDisabled }: { turn: Turn; onReuseUserMessage: (text: string) => void; reuseDisabled: boolean }) {
  const { query } = useUI();
  const userText = textOf(turn.user);
  return (
    <div className="turn" data-turn={turn.index}>
      {turn.user && (
        <div className="user-msg">
          <div className="user-msg-head">
            <span className="who">You</span>
            {userText.trim() && (
              <button
                className="reuse-message"
                type="button"
                title="Fill the composer with this message to edit and resend"
                disabled={reuseDisabled}
                onClick={() => onReuseUserMessage(userText)}
              >
                Edit &amp; resend
              </button>
            )}
          </div>
          <div className="user-msg-text"><Highlighted text={userText} query={query} /></div>
        </div>
      )}
      {turn.notes.map((m, i) => (
        <div className="note" key={i}>
          {m.role}: {firstLine(textOf(m))}
        </div>
      ))}
    </div>
  );
}

function StepView({ step, sub }: { step: LLMStep; sub?: boolean }) {
  const ui = useUI();
  const [showReasoning, setShowReasoning] = useState(false);
  const seq = step.call?.seq;
  const active = seq !== undefined && ui.inspecting === seq;
  const open = () => {
    ui.select(step.key);
    if (seq !== undefined) ui.inspect(seq, step.label);
  };
  return (
    <div>
      <div
        className={`row llm-row ${sub ? "sub" : ""} ${ui.selected === step.key || active ? "selected" : ""}`}
        data-key={step.key}
        onClick={open}
        title={seq !== undefined ? "Open the full model input and output" : "No trace recorded for this call"}
      >
        <span className="caret">◆</span>
        <span className="label">{step.label}</span>
        {step.call?.error && <span className="badge bad">error</span>}
        {step.reasoning && (
          <button
            className="chip"
            onClick={(e) => {
              e.stopPropagation();
              setShowReasoning(!showReasoning);
            }}
          >
            thinking
          </button>
        )}
        <span className="meta">
          {llmMeta(step)}
          {seq !== undefined && <span className="badge">inspect ›</span>}
        </span>
      </div>
      {showReasoning && <div className="reasoning">{step.reasoning}</div>}
      {step.text && (
        <div className="assistant-text">
          {ui.query ? <Highlighted text={step.text} query={ui.query} /> : <Markdown text={step.text} />}
        </div>
      )}
      {step.tools.map((t, i) => (
        <ToolRow key={t.key} step={t} last={i === step.tools.length - 1} />
      ))}
      {step.batch && (
        <div className="batch">
          ⇉ {step.batch.count} tools · wall {formatDuration(step.batch.wallMs)} · sum {formatDuration(step.batch.sumMs)} ·{" "}
          {step.batch.parallel ? "parallel" : "sequential"}
        </div>
      )}
    </div>
  );
}

function Status({ step }: { step: ToolStep }) {
  if (failedCommand(step)) return <span className="status error" title="command exited non-zero">!</span>;
  switch (step.status) {
    case "running":
      return (
        <span className="status">
          <span className="spinner" />
        </span>
      );
    case "ok":
      return <span className="status ok">✓</span>;
    case "error":
      return <span className="status error">✗</span>;
    case "awaiting":
      return <span className="status awaiting">⏸</span>;
    default:
      return <span className="status stopped">■</span>;
  }
}

function ToolRow({ step, last }: { step: ToolStep; last: boolean }) {
  const ui = useUI();
  const open = ui.expanded.has(step.key);
  const added = step.diffs.reduce((a, d) => a + d.added, 0);
  const removed = step.diffs.reduce((a, d) => a + d.removed, 0);
  const label = step.sub ? `◇ task #${step.sub.number}` : step.call.name;
  const canHaveDiff = ["write_file", "edit_file", "apply_patch", "delete"].includes(step.call.name);
  return (
    <div className="nest">
      <div
        className={`row ${ui.selected === step.key ? "selected" : ""}`}
        data-key={step.key}
        onClick={() => {
          ui.select(step.key);
          ui.toggle(step.key);
        }}
      >
        <span className="tree">{last ? "└─" : "├─"}</span>
        <span className={open ? "caret open" : "caret"}>›</span>
        <Status step={step} />
        <span className={`tool-name ${category(step.call.name)}`}>{label}</span>
        <span className="tool-summary" title={summarize(step.call)}>
          <Highlighted text={summarize(step.call)} query={ui.query} />
        </span>
        <span className="meta">
          {step.diffs.length > 0 && <DiffStat added={added} removed={removed} />}
          {canHaveDiff && step.diffs.length === 0 && (
            <button className="copy" type="button" disabled={ui.diffsLoading} onClick={(e) => { e.stopPropagation(); ui.loadDiffs(); }}>
              {ui.diffsLoading ? "loading changes…" : "load changes"}
            </button>
          )}
          <span className={step.status === "error" || failedCommand(step) ? "minus" : ""}>{resultSummary(step)}</span>
          {step.durationMs !== undefined && <span>{formatDuration(step.durationMs)}</span>}
        </span>
      </div>
      {open && <ToolDetail step={step} />}
      {step.sub && <SubRunView run={step.sub} />}
    </div>
  );
}

function SubRunView({ run }: { run: SubRun }) {
  if (run.steps.length === 0) return null;
  return (
    <div className="nest" style={{ borderLeft: "1px solid var(--border)", marginLeft: 30, paddingLeft: 4 }}>
      {run.steps.map((s) => (
        <StepView key={s.key} step={s} sub />
      ))}
      {run.report && (
        <div className="assistant-text muted" style={{ fontSize: 13 }}>
          ↳ {firstLine(run.report)}
        </div>
      )}
    </div>
  );
}

function FilesChanged({ turn }: { turn: Turn }) {
  const ui = useUI();
  const key = `${turn.key}-files`;
  const open = ui.expanded.has(key);
  const files = groupFiles(turn.files);
  const added = files.reduce((a, f) => a + f.added, 0);
  const removed = files.reduce((a, f) => a + f.removed, 0);
  return (
    <div className="files-changed">
      <div className="row" data-key={key} onClick={() => ui.toggle(key)}>
        <span className={open ? "caret open" : "caret"}>›</span>
        <strong>Files changed</strong>
        <span className="badge">{files.length}</span>
        <span className="meta">
          <DiffStat added={added} removed={removed} />
        </span>
      </div>
      {open && (
        <div className="files-body">
          {files.map((f) =>
            f.diffs.map((d, i) => (
              <DiffView
                key={`${f.path}-${i}`}
                unified={d.unified}
                path={f.path}
                added={d.added}
                removed={d.removed}
                label={f.diffs.length > 1 ? `${d.op} ${i + 1}/${f.diffs.length}` : f.created ? "new file" : d.op}
              />
            )),
          )}
        </div>
      )}
      {!open && (
        <div style={{ padding: "4px 10px 6px 30px" }}>
          {files.map((f) => (
            <div key={f.path} className="mono" style={{ display: "flex", gap: 10, fontSize: 12.5 }}>
              <span style={{ flex: 1 }}>
                {f.path}
                {f.created && <span className="badge new" style={{ marginLeft: 6 }}>new</span>}
                {f.deleted && <span className="badge bad" style={{ marginLeft: 6 }}>deleted</span>}
              </span>
              <DiffStat added={f.added} removed={f.removed} />
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function LiveView({ live }: { live: LiveState }) {
  return (
    <div className="live">
      {live.pendingUser && (
        <div className="user-msg">
          <div className="who">You</div>
          {live.pendingUser}
        </div>
      )}
      {live.streamText && (
        <div className="assistant-text">
          <Markdown text={live.streamText} />
        </div>
      )}
      {live.thinking && (
        <div className="thinking">
          <span className="spinner" /> {live.nextLabel} · thinking…
        </div>
      )}
      {live.error && <div className="error-box">{live.error}</div>}
    </div>
  );
}
