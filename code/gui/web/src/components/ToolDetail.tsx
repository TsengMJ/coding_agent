import { useState } from "react";
import type { ToolStep } from "../model/timeline";
import { formatDuration, str, textOf } from "../model/messages";
import { countKinds, diffLines, type Hunk } from "../model/diff";
import { parsePatch } from "../model/patch";
import {
  buildTree,
  commandLine,
  grepMode,
  languageOf,
  parseExecute,
  parseGrep,
  parseLibraries,
  parseNumbered,
  pathList,
  sandboxOf,
  UNSANDBOXED,
  type CodeLine,
  type GrepFile,
  type TreeNode,
} from "../model/tools";
import { Code, Command, CopyButton, Markdown, Section, Tabs, type CodeRow } from "./common";
import { DiffView } from "./DiffView";
import { JsonTree } from "./JsonTree";
import { currentTodos, isMerge, planOf, planID, questionsOf, todosOf } from "../model/plan";
import { doneCount, PlanDoc, TodoList } from "./Plan";

type Tab = "pretty" | "args" | "output" | "raw";

// ToolDetail is the expanded view of one tool call: a readable rendering
// for known tools plus the exact arguments, output and raw JSON.
export function ToolDetail({ step }: { step: ToolStep }) {
  const [tab, setTab] = useState<Tab>("pretty");
  const output = textOf(step.result);
  return (
    <div className="detail" onClick={(e) => e.stopPropagation()}>
      <Tabs<Tab>
        tabs={[
          { id: "pretty", label: "Overview" },
          { id: "args", label: "Arguments" },
          { id: "output", label: "Output" },
          { id: "raw", label: "Raw JSON" },
        ]}
        active={tab}
        onChange={setTab}
        extra={<span className="muted mono" style={{ fontSize: 11.5 }}>call {step.call.id}</span>}
      />
      <div className="detail-body">
        {tab === "pretty" && <Pretty step={step} output={output} />}
        {tab === "args" && <JsonTree value={step.call.args} />}
        {tab === "output" && <RawOutput text={output} step={step} />}
        {tab === "raw" && <JsonTree value={{ call: step.call, result: step.result ?? null, diffs: step.diffs }} />}
      </div>
    </div>
  );
}

function RawOutput({ text, step }: { text: string; step: ToolStep }) {
  if (!step.result) return <div className="muted">No output yet ({step.status}).</div>;
  return (
    <>
      <div style={{ display: "flex", justifyContent: "flex-end", marginBottom: 6 }}>
        <CopyButton text={text} />
      </div>
      <pre className="terminal">{text || "(empty)"}</pre>
    </>
  );
}

function Pretty({ step, output }: { step: ToolStep; output: string }) {
  const failed = step.status === "error";
  const body = (() => {
    switch (step.call.name) {
      case "grep":
        return <GrepView step={step} output={output} />;
      case "read_file":
        return <ReadView step={step} output={output} />;
      case "execute":
        return <ExecView step={step} output={output} />;
      case "edit_file":
      case "write_file":
      case "delete":
      case "apply_patch":
        return <EditView step={step} output={output} />;
      case "ls":
      case "glob":
        return <PathsView step={step} output={output} />;
      case "write_todos":
        return <TodosView step={step} output={output} />;
      case "create_plan":
        return <PlanView step={step} output={output} />;
      case "ask_user":
        return <AskView step={step} output={output} />;
      case "task":
        return <TaskView step={step} />;
      case "fetch_url":
        return <FetchView step={step} output={output} />;
      case "resolve_library_id":
        return <LibrariesView step={step} output={output} />;
      case "query_docs":
        return <DocsView step={step} output={output} />;
      default:
        return <GenericView step={step} output={output} />;
    }
  })();
  return (
    <>
      {failed && <div className="error-box">{output}</div>}
      {!step.result && step.status !== "running" && (
        <div className="muted" style={{ marginBottom: 8 }}>
          {step.status === "awaiting" ? "Waiting for your approval." : "This call did not finish (the run was stopped)."}
        </div>
      )}
      {body}
    </>
  );
}

function Timing({ step }: { step: ToolStep }) {
  return step.durationMs !== undefined ? <span className="badge">{formatDuration(step.durationMs)}</span> : null;
}

function GrepView({ step, output }: { step: ToolStep; output: string }) {
  const mode = grepMode(step.call);
  const pattern = str(step.call.args["pattern"]);
  const files = step.result && step.status !== "error" ? parseGrep(output, mode) : null;
  const matches = files?.reduce((n, f) => n + (f.count ?? 0), 0) ?? 0;
  return (
    <>
      <Section title="Command" extra={<Timing step={step} />}>
        <Command text={commandLine(step.call)} />
      </Section>
      {files && (
        <Section
          title="Result"
          extra={
            <span className="badge">
              {mode === "files_with_matches" ? `${files.length} files` : `${matches} matches in ${files.length} files`}
            </span>
          }
        >
          {files.length === 0 && <div className="muted">No matches.</div>}
          {mode === "files_with_matches" && files.length > 0 && <Tree root={buildTree(files.map((f) => f.path))} />}
          {mode === "count" && <CountBars files={files} />}
          {mode === "content" &&
            files.map((f) => (
              <div className="grep-file" key={f.path}>
                <div className="file-head">
                  <span className="grow">{f.path}</span>
                  <span className="badge">{f.count} matches</span>
                </div>
                <Code rows={grepRows(f)} mark={pattern} />
              </div>
            ))}
        </Section>
      )}
      {step.result && !files && step.status !== "error" && <pre className="terminal">{output}</pre>}
    </>
  );
}

function grepRows(f: GrepFile): CodeRow[] {
  const rows: CodeRow[] = [];
  f.lines.forEach((l, i) => {
    if (f.breaks.includes(i)) rows.push({ text: "", sep: true });
    rows.push({ no: l.no, text: l.text, hit: l.match });
  });
  return rows;
}

function CountBars({ files }: { files: GrepFile[] }) {
  const maxCount = Math.max(1, ...files.map((f) => f.count ?? 0));
  return (
    <div className="kv" style={{ gridTemplateColumns: "minmax(0, max-content) 1fr" }}>
      {files.map((f) => (
        <div key={f.path} style={{ display: "contents" }}>
          <dt className="mono">{f.path}</dt>
          <dd>
            <span
              style={{
                display: "inline-block",
                height: 8,
                width: `${((f.count ?? 0) / maxCount) * 200}px`,
                background: "var(--purple)",
                borderRadius: 4,
                marginRight: 8,
              }}
            />
            {f.count}
          </dd>
        </div>
      ))}
    </div>
  );
}

function ReadView({ step, output }: { step: ToolStep; output: string }) {
  const path = str(step.call.args["file_path"]);
  const lines: CodeLine[] | null = step.result && step.status !== "error" ? parseNumbered(output) : null;
  return (
    <>
      <Section title="Command" extra={<Timing step={step} />}>
        <Command text={commandLine(step.call)} />
      </Section>
      {lines && (
        <Section title="Content" extra={<span className="badge">{lines.length} lines</span>}>
          <div className="file-head">
            <span className="grow">{path}</span>
            <CopyButton text={lines.map((l) => l.text).join("\n")} />
          </div>
          <Code rows={lines.map((l) => ({ no: l.no, text: l.text }))} lang={languageOf(path)} />
        </Section>
      )}
      {step.result && !lines && step.status !== "error" && <pre className="terminal">{output}</pre>}
    </>
  );
}

function ExecView({ step, output }: { step: ToolStep; output: string }) {
  const res = step.result ? parseExecute(output) : null;
  const timeout = step.call.args["timeout"];
  return (
    <>
      <Section title="Command" extra={<Timing step={step} />}>
        <Command text={str(step.call.args["command"])} />
      </Section>
      {timeout !== undefined && <div className="muted">timeout: {str(timeout)}s</div>}
      {res && step.status !== "error" && (
        <Section
          title="Output"
          extra={
            <>
              {res.exitCode !== undefined && (
                <span className={`badge ${res.exitCode === 0 ? "ok" : "bad"}`}>exit {res.exitCode}</span>
              )}{" "}
              {res.truncated && <span className="badge bad">truncated</span>}{" "}
              <SandboxBadge step={step} />
            </>
          }
        >
          <pre className="terminal">{res.output || "(no output)"}</pre>
        </Section>
      )}
    </>
  );
}

function EditView({ step, output }: { step: ToolStep; output: string }) {
  const path = str(step.call.args["file_path"]);
  if (step.diffs.length > 0) {
    return (
      <>
        {step.diffs.map((d, i) => (
          <div key={i} style={{ marginBottom: 10 }}>
            <DiffView unified={d.unified} path={d.path} added={d.added} removed={d.removed} label={d.op} />
          </div>
        ))}
        {step.result && step.status !== "error" && <div className="muted">{output}</div>}
      </>
    );
  }
  // No recorded diff (tracing off, or the call failed): show what the
  // model asked for.
  if (step.call.name === "apply_patch") {
    return (
      <>
        <Section title="Requested change">
          <PatchPreview patch={str(step.call.args["patch"])} label="requested" />
        </Section>
        {step.result && step.status !== "error" && <div className="muted">{output}</div>}
      </>
    );
  }
  const hunk = requestedChange(step);
  return (
    <>
      <Section title="Requested change">
        {hunk ? (
          <DiffView hunks={[hunk]} path={path} {...countKinds(hunk.lines)} label="requested" />
        ) : (
          <JsonTree value={step.call.args} />
        )}
      </Section>
      {step.result && step.status !== "error" && <div className="muted">{output}</div>}
    </>
  );
}

// PatchPreview shows an apply_patch patch as one diff per file; text that
// does not parse is shown as is.
export function PatchPreview({ patch, label }: { patch: string; label: string }) {
  const files = parsePatch(patch);
  if (!files) return <pre className="terminal">{patch}</pre>;
  return (
    <>
      {files.map((f, i) => (
        <div key={i} style={{ marginBottom: 10 }}>
          {f.op === "delete" ? (
            <div className="mono">Deletes {f.path}</div>
          ) : (
            <DiffView
              hunks={f.hunks}
              path={f.moveTo ? `${f.path} → ${f.moveTo}` : f.path}
              {...countKinds(f.hunks.flatMap((h) => h.lines))}
              label={`${label} ${f.op}`}
            />
          )}
        </div>
      ))}
    </>
  );
}

export function requestedChange(step: { call: { name: string; args: Record<string, unknown> } }): Hunk | null {
  const a = step.call.args;
  if (step.call.name === "edit_file") {
    const lines = diffLines(str(a["old_string"]), str(a["new_string"]));
    return { header: "@@ requested replacement @@", oldStart: 1, newStart: 1, lines };
  }
  if (step.call.name === "write_file") {
    const lines = diffLines("", str(a["content"]));
    return { header: "@@ new content @@", oldStart: 1, newStart: 1, lines };
  }
  return null;
}

function PathsView({ step, output }: { step: ToolStep; output: string }) {
  const paths = step.result && step.status !== "error" ? pathList(output) : [];
  return (
    <>
      <Section title="Command" extra={<Timing step={step} />}>
        <Command text={commandLine(step.call)} />
      </Section>
      {step.result && step.status !== "error" && (
        <Section title="Result" extra={<span className="badge">{paths.length} entries</span>}>
          {paths.length === 0 ? <div className="muted">Nothing found.</div> : <Tree root={buildTree(paths)} />}
        </Section>
      )}
    </>
  );
}

function Tree({ root }: { root: TreeNode }) {
  const render = (n: TreeNode, prefix: string): React.ReactNode[] =>
    n.children.flatMap((c, i) => {
      const last = i === n.children.length - 1;
      const line = (
        <div key={c.path}>
          <span style={{ color: "var(--border)" }}>{prefix + (last ? "└─ " : "├─ ")}</span>
          <span className={c.dir ? "dir" : ""}>{c.name + (c.dir ? "/" : "")}</span>
        </div>
      );
      return [line, ...render(c, prefix + (last ? "   " : "│  "))];
    });
  return <div className="ftree">{render(root, "")}</div>;
}

function TodosView({ step, output }: { step: ToolStep; output: string }) {
  const todos = currentTodos(step.call, step.result && step.status !== "error" ? output : undefined);
  const changed = isMerge(step.call) ? new Set(todosOf(step.call).map((t) => t.id)) : null;
  return (
    <Section
      title={changed ? `Todos · updated ${changed.size}` : "Todos"}
      extra={<span className="badge">{doneCount(todos)}</span>}
    >
      <TodoList todos={todos} />
    </Section>
  );
}

function PlanView({ step, output }: { step: ToolStep; output: string }) {
  const id = step.result && step.status !== "error" ? planID(output) : "";
  return <PlanDoc plan={planOf(step.call)} id={id} />;
}

function AskView({ step, output }: { step: ToolStep; output: string }) {
  const qs = questionsOf(step.call.args) ?? [];
  return (
    <>
      <Section title="Questions">
        <ol className="ask-list">
          {qs.map((q) => (
            <li key={q.id}>
              {q.prompt}
              <span className="muted"> — {q.options.map((o) => o.label).join(" / ")}</span>
            </li>
          ))}
        </ol>
      </Section>
      {step.result && (
        <Section title={step.status === "error" ? "Skipped" : "Answers"}>
          <pre className="terminal">{output}</pre>
        </Section>
      )}
    </>
  );
}

function TaskView({ step }: { step: ToolStep }) {
  return (
    <>
      <Section title={`Subagent · ${str(step.call.args["subagent_type"])}`} extra={
          <>
            {step.sub?.background && <span className="badge">background</span>} <Timing step={step} />
          </>
        }>
        <div className="md user-msg" style={{ margin: 0 }}>
          <Markdown text={str(step.call.args["description"])} />
        </div>
      </Section>
      {step.sub?.report && (
        <Section title="Report">
          <Markdown text={step.sub.report} />
        </Section>
      )}
    </>
  );
}

function GenericView({ step, output }: { step: ToolStep; output: string }) {
  return (
    <>
      <Section title="Arguments" extra={<Timing step={step} />}>
        <JsonTree value={step.call.args} />
      </Section>
      {step.result && step.status !== "error" && (
        <Section title="Output" extra={<SandboxBadge step={step} />}>
          <pre className="terminal">{output || "(empty)"}</pre>
        </Section>
      )}
    </>
  );
}

// SandboxBadge shows the sandbox a shell tool ran under; escapes and
// suspected denials are highlighted.
function SandboxBadge({ step }: { step: ToolStep }) {
  const sb = sandboxOf(step.result);
  if (!sb) return null;
  if (sb.mode === UNSANDBOXED) {
    return <span className="badge bad" title="Ran outside the sandbox after your approval">unsandboxed</span>;
  }
  return (
    <>
      <span className="badge" title="Ran inside the OS sandbox">sandbox: {sb.mode}</span>
      {sb.blocked && (
        <>
          {" "}
          <span className="badge bad" title="The failure looks like a sandbox denial">sandbox blocked</span>
        </>
      )}
    </>
  );
}

function LibrariesView({ step, output }: { step: ToolStep; output: string }) {
  const libs = step.result && step.status !== "error" ? parseLibraries(output) : null;
  return (
    <>
      <Section title="Context7 search" extra={<Timing step={step} />}>
        <div className="mono">
          {str(step.call.args["library_name"])} · {str(step.call.args["query"])}
        </div>
      </Section>
      {libs && (
        <Section title="Libraries" extra={<span className="badge">{libs.length} found</span>}>
          <ol className="ask-list">
            {libs.map((l) => (
              <li key={l.id}>
                <strong>{l.title}</strong> <span className="mono">{l.id}</span>
                {l.description && <div>{l.description}</div>}
                <div className="muted">
                  {l.stats}
                  {l.versions && ` · versions ${l.versions}`}
                </div>
              </li>
            ))}
          </ol>
        </Section>
      )}
      {step.result && !libs && step.status !== "error" && <pre className="terminal">{output}</pre>}
    </>
  );
}

function DocsView({ step, output }: { step: ToolStep; output: string }) {
  return (
    <>
      <Section title="Context7 docs" extra={<Timing step={step} />}>
        <div className="mono">
          {str(step.call.args["library_id"])} · {str(step.call.args["query"])}
        </div>
      </Section>
      {step.result && step.status !== "error" && (
        <Section title="Documentation">
          <Markdown text={output} />
        </Section>
      )}
    </>
  );
}

function FetchView({ step, output }: { step: ToolStep; output: string }) {
  const x = step.extract;
  return (
    <>
      <Section title="Fetch" extra={<Timing step={step} />}>
        <div className="mono">{str(step.call.args["url"])}</div>
        {str(step.call.args["prompt"]) && <div className="muted">Prompt: {str(step.call.args["prompt"])}</div>}
      </Section>
      {x && (
        <Section title="Extraction" extra={<span className="badge">{formatDuration(x.duration_ms)}</span>}>
          <div className="muted">
            LLM #{x.seq}
            {x.model && ` · ${x.model}`}
            {x.usage && ` · ${x.usage.input_tokens} in / ${x.usage.output_tokens} out`}
            {x.error && ` · ${x.error}`}
          </div>
        </Section>
      )}
      {step.result && step.status !== "error" && (
        <Section title={x ? "Answer" : "Content"}>
          <Markdown text={output} />
        </Section>
      )}
    </>
  );
}
