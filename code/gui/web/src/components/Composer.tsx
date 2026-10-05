import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import type { RunMode, SlashCommand } from "../types";
import { matchCommands, usage } from "../model/commands";

// Composer is the message box. Enter sends, Shift+Enter adds a line.
// Typing "/" opens slash-command completion: ↑/↓ pick, Tab completes.
// Commands are run by the page.
export const Composer = forwardRef<
  HTMLTextAreaElement,
  {
    running: boolean;
    blocked: boolean;
    commands: SlashCommand[];
    mode: RunMode;
    text: string;
    literal: boolean;
    replacePending: boolean;
    onTextChange: (text: string, literal: boolean) => void;
    onReplaceConfirm: () => void;
    onReplaceCancel: () => void;
    onMode: (m: RunMode) => void;
    onSend: (text: string, literal: boolean) => void;
    onStop: () => void;
  }
>(function Composer({ running, blocked, commands, mode, text, literal, replacePending, onTextChange, onReplaceConfirm, onReplaceCancel, onMode, onSend, onStop }, ref) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => textareaRef.current!, []);
  const [pick, setPick] = useState(0);
  const matches = literal ? [] : matchCommands(text, commands);
  const picked = matches[Math.min(pick, matches.length - 1)];
  const sizeTextarea = () => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 240)}px`;
  };
  useLayoutEffect(() => {
    sizeTextarea();
  }, [text]);
  useEffect(() => {
    const el = textareaRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    let width = el.clientWidth;
    const observer = new ResizeObserver(() => {
      if (el.clientWidth === width) return;
      width = el.clientWidth;
      sizeTextarea();
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  const send = () => {
    if (!text.trim() || running || blocked) return;
    const t = literal ? text : text.trim();
    onSend(t, literal);
    onTextChange("", false);
  };
  const complete = (c: SlashCommand) => {
    onTextChange(c.args ? `${c.name} ` : c.name, false);
    setPick(0);
  };
  return (
    <div className="composer">
      {matches.length > 0 && (
        <ul className="command-menu" role="listbox">
          {matches.map((c) => (
            <li
              key={c.name}
              role="option"
              aria-selected={c === picked}
              className={c === picked ? "picked" : ""}
              onMouseDown={(e) => {
                e.preventDefault();
                complete(c);
              }}
            >
              <span className="mono">{usage(c)}</span> <span className="muted">{c.description}</span>
            </li>
          ))}
        </ul>
      )}
      {replacePending && (
        <div className="draft-replace" role="status">
          <span>Replace your current draft with this earlier message?</span>
          <button type="button" onClick={onReplaceConfirm} disabled={running || blocked}>Replace draft</button>
          <button type="button" onClick={onReplaceCancel}>Cancel</button>
        </div>
      )}
      <div className={`composer-box ${mode === "plan" ? "plan-mode" : ""}`}>
        <textarea
          ref={textareaRef}
          rows={3}
          placeholder={
            blocked
              ? "Answer the approval above first"
              : mode === "plan"
                ? "Describe what to plan — quill researches read-only and writes a plan (/ for commands)"
                : "Ask quill… (/ for commands)"
          }
          value={text}
          onChange={(e) => {
            onTextChange(e.target.value, literal);
            setPick(0);
          }}
          onKeyDown={(e) => {
            if (matches.length > 0 && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
              e.preventDefault();
              const step = e.key === "ArrowDown" ? 1 : -1;
              setPick((p) => (Math.min(p, matches.length - 1) + step + matches.length) % matches.length);
              return;
            }
            if (matches.length > 0 && e.key === "Tab" && picked) {
              e.preventDefault();
              complete(picked);
              return;
            }
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              send();
            }
          }}
        />
        <div className="composer-actions">
          <div className="mode-toggle" role="radiogroup" aria-label="mode">
            {(["agent", "plan"] as const).map((m) => (
              <button
                key={m}
                role="radio"
                aria-checked={mode === m}
                className={mode === m ? "active" : ""}
                disabled={running}
                onClick={() => onMode(m)}
                title={m === "plan" ? "Plan mode: read-only research, then a plan to review before any change" : "Agent mode: make the changes"}
              >
                {m === "plan" ? "Plan" : "Agent"}
              </button>
            ))}
          </div>
          {literal && <span className="composer-origin">From history · sends as message</span>}
          <span className="composer-spacer" />
          {running ? (
            <button className="btn danger composer-submit" type="button" onClick={onStop}>Stop</button>
          ) : (
            <button className="btn primary composer-submit" type="button" onClick={send} disabled={!text.trim() || blocked}>Send</button>
          )}
        </div>
      </div>
      <div className="hint">
        <kbd>Enter</kbd> send · <kbd>Shift</kbd>+<kbd>Enter</kbd> newline · <kbd>/</kbd> commands (<kbd>Tab</kbd> completes)
      </div>
    </div>
  );
});
