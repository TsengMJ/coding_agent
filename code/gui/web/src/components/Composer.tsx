import { forwardRef, useState } from "react";

// Composer is the message box. Enter sends, Shift+Enter adds a line;
// /new and /compact are handled by the page.
export const Composer = forwardRef<
  HTMLTextAreaElement,
  { running: boolean; blocked: boolean; onSend: (text: string) => void; onStop: () => void }
>(function Composer({ running, blocked, onSend, onStop }, ref) {
  const [text, setText] = useState("");
  const send = () => {
    const t = text.trim();
    if (!t || running || blocked) return;
    onSend(t);
    setText("");
  };
  return (
    <div className="composer">
      <div className="composer-box">
        <textarea
          ref={ref}
          rows={Math.min(8, Math.max(1, text.split("\n").length))}
          placeholder={blocked ? "Answer the approval above first" : "Ask dcode… (/new, /compact)"}
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
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
        <kbd>Enter</kbd> send · <kbd>Shift</kbd>+<kbd>Enter</kbd> newline · <kbd>Esc</kbd> leave box, then <kbd>j</kbd>/
        <kbd>k</kbd> move · <kbd>Enter</kbd> expand · <kbd>e</kbd> expand all · <kbd>/</kbd> search
      </div>
    </div>
  );
});
