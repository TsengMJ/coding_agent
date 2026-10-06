import { forwardRef, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState } from "react";
import type { RunMode, SlashCommand } from "../types";
import { matchCommands, usage } from "../model/commands";
import { IMAGE_TYPES, imageError, imageSrc, readImage, type ImageAttachment } from "../model/images";

// Composer is the message box. Enter sends, Shift+Enter adds a line.
// Typing "/" opens slash-command completion: ↑/↓ pick, Tab completes.
// Images can be pasted, dropped or attached with the button.
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
    onSend: (text: string, literal: boolean, images: ImageAttachment[]) => void;
    onStop: () => void;
  }
>(function Composer({ running, blocked, commands, mode, text, literal, replacePending, onTextChange, onReplaceConfirm, onReplaceCancel, onMode, onSend, onStop }, ref) {
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  useImperativeHandle(ref, () => textareaRef.current!, []);
  const [pick, setPick] = useState(0);
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [imageNotice, setImageNotice] = useState("");
  const [dragging, setDragging] = useState(false);
  const addImages = async (files: File[]) => {
    const added: ImageAttachment[] = [];
    const errors: string[] = [];
    for (const f of files) {
      const err = imageError(f, [...images, ...added]);
      if (err) {
        errors.push(err);
        continue;
      }
      try {
        added.push(await readImage(f));
      } catch (e) {
        errors.push(`${f.name}: ${(e as Error).message}`);
      }
    }
    if (added.length) setImages((old) => [...old, ...added]);
    setImageNotice(errors.join("\n"));
  };
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
  const canSend = !!text.trim() || images.length > 0;
  const send = () => {
    if (!canSend || running || blocked) return;
    const t = literal ? text : text.trim();
    onSend(t, literal, images);
    onTextChange("", false);
    setImages([]);
    setImageNotice("");
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
      {imageNotice && (
        <div className="image-notice" role="alert">
          <span>{imageNotice}</span>
          <button type="button" onClick={() => setImageNotice("")}>Dismiss</button>
        </div>
      )}
      <div
        className={`composer-box ${mode === "plan" ? "plan-mode" : ""} ${dragging ? "dragging" : ""}`}
        onDragOver={(e) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          if (!e.dataTransfer.files.length) return;
          e.preventDefault();
          setDragging(false);
          void addImages([...e.dataTransfer.files]);
        }}
      >
        {images.length > 0 && (
          <div className="composer-images">
            {images.map((img, i) => (
              <div className="composer-image" key={i} title={img.name}>
                <img src={imageSrc({ type: "image", mime_type: img.mimeType, base64: img.base64 })} alt={img.name} />
                <button
                  type="button"
                  aria-label={`Remove ${img.name}`}
                  onClick={() => setImages((old) => old.filter((_, j) => j !== i))}
                >
                  ×
                </button>
              </div>
            ))}
          </div>
        )}
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
          onPaste={(e) => {
            const files = [...e.clipboardData.files];
            if (files.length === 0) return;
            e.preventDefault();
            void addImages(files);
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
          <button
            className="btn composer-attach"
            type="button"
            title="Attach images (or paste / drop them here)"
            disabled={blocked}
            onClick={() => fileRef.current?.click()}
          >
            Attach image
          </button>
          <input
            ref={fileRef}
            type="file"
            accept={IMAGE_TYPES.join(",")}
            multiple
            hidden
            onChange={(e) => {
              void addImages([...(e.target.files ?? [])]);
              e.target.value = "";
            }}
          />
          {literal && <span className="composer-origin">From history · sends as message</span>}
          <span className="composer-spacer" />
          {running ? (
            <button className="btn danger composer-submit" type="button" onClick={onStop}>Stop</button>
          ) : (
            <button className="btn primary composer-submit" type="button" onClick={send} disabled={!canSend || blocked}>Send</button>
          )}
        </div>
      </div>
      <div className="hint">
        <kbd>Enter</kbd> send · <kbd>Shift</kbd>+<kbd>Enter</kbd> newline · <kbd>/</kbd> commands (<kbd>Tab</kbd> completes) · paste or drop images
      </div>
    </div>
  );
});
