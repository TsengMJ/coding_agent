import { forwardRef, useState } from "react";
import type { SlashCommand } from "../types";
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
    onSend: (text: string) => void;
    onStop: () => void;
  }
>(function Composer({ running, blocked, commands, onSend, onStop }, ref) {
  const [text, setText] = useState("");
  const [pick, setPick] = useState(0);
  const matches = matchCommands(text, commands);
  const picked = matches[Math.min(pick, matches.length - 1)];
  const send = () => {
    const t = text.trim();
    if (!t || running || blocked) return;
    onSend(t);
    setText("");
  };
  const complete = (c: SlashCommand) => {
    setText(c.args ? `${c.name} ` : c.name);
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
      <div className="composer-box">
        <textarea
          ref={ref}
          rows={Math.min(8, Math.max(1, text.split("\n").length))}
          placeholder={blocked ? "Answer the approval above first" : "Ask dcode… (/ for commands)"}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
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
        {running ? (
          <button className="btn danger" onClick={onStop}>
            Stop
          </button>
        ) : (
          <button className="btn primary" onClick={send} disabled={!text.trim() || blocked}>
            Send
          </button>
        )}
      </div>
      <div className="hint">
        <kbd>Enter</kbd> send · <kbd>Shift</kbd>+<kbd>Enter</kbd> newline · <kbd>/</kbd> commands (<kbd>Tab</kbd>{" "}
        completes) · <kbd>Esc</kbd> leave box, then <kbd>j</kbd>/<kbd>k</kbd> move · <kbd>Enter</kbd> expand ·{" "}
        <kbd>e</kbd> expand all · <kbd>/</kbd> search
      </div>
    </div>
  );
});
