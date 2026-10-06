import { describe, expect, it } from "vitest";
import type { Message, RunEvent } from "../types";
import { applyEvent, emptyView, endRun, fromHistory, startRun } from "./state";
import { buildTimeline, groupFiles } from "./timeline";
import { diffLines, gapBefore, parseUnified, splitRows } from "./diff";
import { failedCommand, matchTurn } from "./filter";
import { buildTree, commandLine, parseExecute, parseGrep, parseLibraries, parseNumbered, sandboxNote, sandboxOf, summarize } from "./tools";

const user = (text: string): Message => ({ role: "user", content: [{ type: "text", text }] });
const assistant = (text: string, calls: Message["tool_calls"] = [], seq?: number): Message => ({
  role: "assistant",
  content: text ? [{ type: "text", text }] : [],
  tool_calls: calls,
  usage: { input_tokens: 100, output_tokens: 10, total_tokens: 110 },
  metadata: seq ? { trace_seq: seq, started_at_ms: 1000, duration_ms: 500 } : undefined,
});
const toolMsg = (id: string, text: string, start = 0, dur = 0, status: "success" | "error" = "success"): Message => ({
  role: "tool",
  tool_call_id: id,
  status,
  content: [{ type: "text", text }],
  metadata: { started_at_ms: start, duration_ms: dur },
});
const ev = (e: Partial<RunEvent>): RunEvent => ({ event: "message", ...e }) as RunEvent;

describe("applyEvent", () => {
  it("streams text and clears it when the reply lands", () => {
    let v = startRun(emptyView(), "hi");
    v = applyEvent(v, { event: "delta", text: "Hel" });
    v = applyEvent(v, { event: "delta", text: "lo" });
    expect(v.streamText).toBe("Hello");
    v = applyEvent(v, ev({ message: user("hi") }));
    expect(v.pendingUser).toBe("");
    v = applyEvent(v, ev({ message: assistant("Hello") }));
    expect(v.streamText).toBe("");
    expect(v.messages).toHaveLength(2);
  });

  it("keeps subagent messages and deltas out of the main list", () => {
    let v = emptyView();
    v = applyEvent(v, { event: "delta", text: "sub words", namespace: ["task:t1"] });
    v = applyEvent(v, ev({ message: assistant("sub"), namespace: ["task:t1"] }));
    expect(v.streamText).toBe("");
    expect(v.messages).toHaveLength(0);
    expect(v.sub["task:t1"]).toHaveLength(1);
  });

  it("indexes trace updates", () => {
    let v = emptyView();
    v = applyEvent(v, {
      event: "trace",
      trace: { llm_call: { seq: 3, start: "", duration_ms: 5, message_count: 2, new_from: 0, tool_count: 1 } },
    });
    v = applyEvent(v, {
      event: "trace",
      trace: { diff: { path: "/a", op: "edit", unified: "", added: 1, removed: 0, time: "" } },
    });
    expect(v.llm[3].duration_ms).toBe(5);
    expect(v.diffs).toHaveLength(1);
  });

  it("ignores replayed messages and diffs after reattaching to a run", () => {
    const reply = { ...assistant("done"), id: "a1" };
    const diff = { tool_call_id: "c1", path: "/a", op: "edit" as const, unified: "", added: 1, removed: 0, time: "t" };
    const prompt = { ...user("hi"), id: "u1" };
    let v = fromHistory({ thread_id: "t", messages: [prompt, reply, toolMsg("c1", "ok")] }, null);
    v = { ...v, diffs: [diff] };
    v = startRun(v, "");
    for (const e of [ev({ message: prompt }), { event: "delta", text: "do" } as RunEvent, ev({ message: reply }),
      ev({ message: toolMsg("c1", "ok") }), { event: "trace", trace: { diff } } as RunEvent,
      ev({ message: assistant("sub"), namespace: ["task:t1"] }), ev({ message: assistant("sub"), namespace: ["task:t1"] })]) {
      v = applyEvent(v, e);
    }
    expect(v.messages).toHaveLength(3);
    expect(v.diffs).toHaveLength(1);
    expect(v.sub["task:t1"]).toHaveLength(1);
    expect(v.streamText).toBe("");
    v = applyEvent(v, ev({ message: { ...assistant("next"), id: "a2" } }));
    expect(v.messages).toHaveLength(4);
  });

  it("records interrupts and errors, and endRun stops running", () => {
    let v = startRun(emptyView(), "x");
    v = applyEvent(v, { event: "interrupt", interrupts: [{ id: "i", action_requests: [{ name: "execute", args: {} }] }] });
    v = applyEvent(v, { event: "error", error: "boom" });
    v = endRun(v, v.error);
    expect(v.interrupts).toHaveLength(1);
    expect(v.running).toBe(false);
    expect(v.error).toBe("boom");
  });
});

describe("buildTimeline", () => {
  const history = () =>
    fromHistory(
      {
        thread_id: "t",
        messages: [
          user("find handlers"),
          assistant(
            "looking",
            [
              { id: "g", name: "grep", args: { pattern: "HandleFunc" } },
              { id: "e", name: "edit_file", args: { file_path: "/a.go" } },
            ],
            1,
          ),
          toolMsg("g", "/a.go", 0, 100),
          toolMsg("e", "ok", 10, 100),
          assistant("", [{ id: "t1", name: "task", args: { subagent_type: "general-purpose", description: "dig" } }], 2),
          toolMsg("t1", "sub report"),
          assistant("done", [], 5),
          user("again"),
          assistant("sure"),
        ],
      },
      {
        thread_id: "t",
        llm_calls: [{ seq: 1, start: "", duration_ms: 500, message_count: 2, new_from: 0, tool_count: 7 }],
        diffs: [
          { tool_call_id: "e", path: "/a.go", op: "edit", unified: "", added: 2, removed: 1, time: "" },
          { tool_call_id: "w", namespace: ["task:t1"], path: "/b.go", op: "write", unified: "--- /dev/null\n", added: 3, removed: 0, time: "" },
        ],
        messages: [
          { namespace: ["task:t1"], message: user("dig") },
          { namespace: ["task:t1"], message: assistant("", [{ id: "w", name: "write_file", args: { file_path: "/b.go" } }], 3) },
          { namespace: ["task:t1"], message: toolMsg("w", "written") },
          { namespace: ["task:t1"], message: assistant("sub report", [], 4) },
        ],
      },
    );

  it("splits turns at user messages and numbers LLM calls like the TUI", () => {
    const tl = buildTimeline(history());
    expect(tl.turns).toHaveLength(2);
    expect(tl.turns[0].steps.map((s) => s.label)).toEqual(["main · LLM #1", "main · LLM #2", "main · LLM #3"]);
    expect(tl.turns[1].steps[0].label).toBe("main · LLM #4");
  });

  it("attaches results, durations, diffs and the traced call", () => {
    const step = buildTimeline(history()).turns[0].steps[0];
    expect(step.call?.tool_count).toBe(7);
    const [grep, edit] = step.tools;
    expect(grep.status).toBe("ok");
    expect(grep.durationMs).toBe(100);
    expect(edit.diffs).toHaveLength(1);
    expect(step.batch?.parallel).toBe(true);
  });

  it("nests subagent steps under the task call", () => {
    const task = buildTimeline(history()).turns[0].steps[1].tools[0];
    expect(task.sub?.number).toBe(1);
    expect(task.sub?.steps.map((s) => s.label)).toEqual(["#1 · LLM 1", "#1 · LLM 2"]);
    expect(task.sub?.steps[0].tools[0].status).toBe("ok");
    expect(task.sub?.report).toBe("sub report");
  });

  it("collects files changed in the turn, subagents included", () => {
    const files = groupFiles(buildTimeline(history()).turns[0].files);
    expect(files.map((f) => f.path)).toEqual(["/a.go", "/b.go"]);
    expect(files[1].created).toBe(true);
  });

  it("marks unanswered calls running, awaiting or stopped", () => {
    const v = fromHistory(
      {
        thread_id: "t",
        messages: [user("x"), assistant("", [{ id: "x1", name: "execute", args: { command: "ls" } }])],
        interrupts: [{ id: "i", action_requests: [{ name: "execute", args: { command: "ls" } }] }],
      },
      null,
    );
    expect(buildTimeline(v).turns[0].steps[0].tools[0].status).toBe("awaiting");
    expect(buildTimeline({ ...v, running: true }).turns[0].steps[0].tools[0].status).toBe("running");
    expect(buildTimeline({ ...v, interrupts: [] }).turns[0].steps[0].tools[0].status).toBe("stopped");
  });

  it("sums tokens over main and subagent replies", () => {
    expect(buildTimeline(history()).tokens).toBe(110 * 6);
  });

  const notice = (id: string, status: string, result: string) =>
    user(
      `<task-notification>\n<task-id>${id}</task-id>\n<subagent-type>web-researcher</subagent-type>\n<status>${status}</status>\n<result>\n${result}\n</result>\n</task-notification>`,
    );
  const bgHistory = (...rest: Message[]) =>
    fromHistory(
      {
        thread_id: "t",
        messages: [
          user("research"),
          assistant("", [{ id: "b1", name: "task", args: { subagent_type: "web-researcher", description: "pgx", run_in_background: true } }]),
          toolMsg("b1", "Started background task b1 (web-researcher)."),
          ...rest,
        ],
      },
      null,
    );

  it("keeps a background task running until its notification arrives", () => {
    const running = buildTimeline({ ...bgHistory(), running: true }).turns[0].steps[0].tools[0];
    expect(running.status).toBe("running");
    expect(running.sub?.background).toBe(true);
    expect(running.sub?.report).toBeUndefined();

    const tl = buildTimeline(bgHistory(notice("b1", "completed", "pgx v5.7"), assistant("done")));
    expect(tl.turns).toHaveLength(1);
    const done = tl.turns[0].steps[0].tools[0];
    expect(done.status).toBe("ok");
    expect(done.sub?.report).toBe("pgx v5.7");
    expect(tl.turns[0].steps.map((s) => s.text)).toEqual(["", "done"]);
  });

  it("attaches the web_extract model call to its fetch_url step", () => {
    const v = fromHistory(
      {
        thread_id: "t",
        messages: [
          user("read"),
          assistant("", [{ id: "f1", name: "fetch_url", args: { url: "https://go.dev", prompt: "version" } }]),
          toolMsg("f1", "Go 1.27"),
        ],
      },
      {
        thread_id: "t",
        llm_calls: [{ seq: 9, namespace: ["web_extract:f1"], model: "haiku", start: "", duration_ms: 300, message_count: 1, new_from: 0, tool_count: 0 }],
        diffs: [],
        messages: [],
      },
    );
    const fetch = buildTimeline(v).turns[0].steps[0].tools[0];
    expect(fetch.extract?.seq).toBe(9);
    expect(fetch.extract?.model).toBe("haiku");
  });

  it("marks failed background tasks as errors and stopped ones without a notification", () => {
    expect(buildTimeline(bgHistory(notice("b1", "failed", "boom"))).turns[0].steps[0].tools[0].status).toBe("error");
    expect(buildTimeline(bgHistory()).turns[0].steps[0].tools[0].status).toBe("stopped");
  });
});

describe("diff", () => {
  const unified = [
    "--- /a.go",
    "+++ /a.go",
    "@@ -1,3 +1,3 @@",
    " package a",
    "-func A() {}",
    "+func A() int { return 1 }",
    " ",
    "@@ -10,2 +10,3 @@",
    " x",
    "+y",
    " z",
    "",
  ].join("\n");

  it("parses hunks with line numbers", () => {
    const d = parseUnified(unified);
    expect(d.oldPath).toBe("/a.go");
    expect(d.hunks).toHaveLength(2);
    const [ctx, del, add] = d.hunks[0].lines;
    expect([ctx.oldNo, ctx.newNo]).toEqual([1, 1]);
    expect(del).toMatchObject({ kind: "del", oldNo: 2 });
    expect(add).toMatchObject({ kind: "add", newNo: 2 });
    expect(d.hunks[1].lines[1]).toMatchObject({ kind: "add", newNo: 11 });
  });

  it("measures the gap between hunks", () => {
    const d = parseUnified(unified);
    expect(gapBefore(undefined, d.hunks[0])).toBe(0);
    expect(gapBefore(d.hunks[0], d.hunks[1])).toBe(6);
  });

  it("pairs deletions with additions for the split view", () => {
    const rows = splitRows(parseUnified(unified).hunks[0].lines);
    expect(rows[1].left?.text).toBe("func A() {}");
    expect(rows[1].right?.text).toBe("func A() int { return 1 }");
  });

  it("diffs lines for approval previews", () => {
    const lines = diffLines("a\nb\nc\n", "a\nB\nc\n");
    expect(lines.filter((l) => l.kind === "ctx").map((l) => l.text)).toEqual(["a", "c"]);
    expect(lines.filter((l) => l.kind === "del").map((l) => l.text)).toEqual(["b"]);
    expect(lines.filter((l) => l.kind === "add").map((l) => l.text)).toEqual(["B"]);
  });

  it("lists a replaced line's deletion before its addition, like git", () => {
    expect(diffLines("old\n", "new\n").map((l) => l.kind)).toEqual(["del", "add"]);
  });
});

describe("tool output", () => {
  it("parses grep content with context", () => {
    const out = "/a.go:\n  3- before\n  4: match here\n  --\n  9: again";
    const files = parseGrep(out, "content")!;
    expect(files[0].path).toBe("/a.go");
    expect(files[0].count).toBe(2);
    expect(files[0].lines[1]).toEqual({ no: 4, match: true, text: "match here" });
    expect(files[0].breaks).toEqual([2]);
  });

  it("parses grep counts and file lists", () => {
    expect(parseGrep("/a: 2\n/b: 5", "count")!.map((f) => f.count)).toEqual([2, 5]);
    expect(parseGrep("/a\n/b", "files_with_matches")!).toHaveLength(2);
    expect(parseGrep("No matches found", "content")).toEqual([]);
  });

  it("parses numbered read_file output", () => {
    const lines = parseNumbered(" 9  package a\n10  \n11  func A() {}")!;
    expect(lines.map((l) => l.no)).toEqual(["9", "10", "11"]);
    expect(lines[1].text).toBe("");
    expect(parseNumbered("Error: file not found")).toBeNull();
  });

  it("splits execute output from its exit code", () => {
    expect(parseExecute("hi\n[Command failed with exit code 2]")).toEqual({ output: "hi", exitCode: 2, truncated: false });
    expect(parseExecute("x\n[Command succeeded with exit code 0]\n[Output was truncated due to size limits]").truncated).toBe(true);
  });

  it("builds a directory tree", () => {
    const root = buildTree(["/src/a.go", "/src/b/", "/README.md"]);
    expect(root.children.map((c) => c.name)).toEqual(["src", "README.md"]);
    expect(root.children[0].children.map((c) => `${c.name}:${c.dir}`)).toEqual(["b:true", "a.go:false"]);
  });

  it("renders the equivalent command of a grep call", () => {
    const call = { id: "1", name: "grep", args: { pattern: "Handle Func", path: "/src", output_mode: "content", glob: "*.go" } };
    expect(commandLine(call)).toBe("grep -r -F -n --include='*.go' 'Handle Func' /src");
    expect(summarize(call)).toBe('"Handle Func" in /src (*.go)');
  });

  it("summarizes web and Context7 calls", () => {
    const call = (name: string, args: Record<string, unknown>) => summarize({ id: "1", name, args });
    expect(call("web_search", { query: "pgx v5", count: 8 })).toBe("pgx v5");
    expect(call("fetch_url", { url: "https://go.dev", prompt: "latest version" })).toBe("https://go.dev · latest version");
    expect(call("resolve_library_id", { library_name: "next.js", query: "routing" })).toBe("next.js · routing");
    expect(call("query_docs", { library_id: "/vercel/next.js", query: "middleware" })).toBe("/vercel/next.js · middleware");
    expect(call("task_output", { task_id: "b1" })).toBe("b1");
    expect(call("task", { subagent_type: "web-researcher", description: "pgx", run_in_background: true })).toBe(
      "web-researcher · pgx · background",
    );
  });

  it("parses Context7 library lists", () => {
    const out = [
      'Context7 libraries for "next.js" (pass the ID to query_docs):',
      "",
      "1. Next.js — /vercel/next.js",
      "   The React framework",
      "   Snippets: 3000 · Trust: 10 · Benchmark: 90",
      "   Versions: v15.1.0, v14.2.0 (pin one as /vercel/next.js@v15.1.0)",
      "2. Next Auth — /nextauthjs/next-auth",
      "   Snippets: 400 · Trust: 8",
    ].join("\n");
    expect(parseLibraries(out)).toEqual([
      { title: "Next.js", id: "/vercel/next.js", description: "The React framework", stats: "Snippets: 3000 · Trust: 10 · Benchmark: 90", versions: "v15.1.0, v14.2.0" },
      { title: "Next Auth", id: "/nextauthjs/next-auth", description: "", stats: "Snippets: 400 · Trust: 8", versions: "" },
    ]);
    expect(parseLibraries("no libraries")).toBeNull();
  });
});

describe("sandboxOf", () => {
  const withMeta = (metadata?: Record<string, unknown>): Message => ({ role: "tool", content: [], metadata });

  it("reads the metadata tag", () => {
    expect(sandboxOf(withMeta({ sandbox: "workspace-write", sandbox_blocked: true }))).toEqual({ mode: "workspace-write", blocked: true });
  });

  it("falls back to the execute artifact of older threads", () => {
    expect(sandboxOf(withMeta({ artifact: { exit_code: 0, sandbox: "unsandboxed" } }))).toEqual({ mode: "unsandboxed", blocked: false });
  });

  it("is null without a tag", () => {
    expect(sandboxOf(withMeta({ artifact: { exit_code: 0 } }))).toBeNull();
    expect(sandboxOf(undefined)).toBeNull();
  });

  it("marks only escapes and denials in the row note", () => {
    expect(sandboxNote({ mode: "workspace-write", blocked: false })).toBe("");
    expect(sandboxNote({ mode: "workspace-write", blocked: true })).toBe("sandbox blocked");
    expect(sandboxNote({ mode: "unsandboxed", blocked: false })).toBe("unsandboxed");
  });
});

describe("matchTurn", () => {
  const turn = () =>
    buildTimeline(
      fromHistory(
        {
          thread_id: "t",
          messages: [
            user("run it"),
            assistant("", [{ id: "x", name: "execute", args: { command: "make test" } }], 1),
            toolMsg("x", "FAIL\n[Command failed with exit code 2]"),
            assistant("tests fail", [], 2),
          ],
        },
        null,
      ),
    ).turns[0];

  it("treats a non-zero exit as an error", () => {
    expect(failedCommand(turn().steps[0].tools[0])).toBe(true);
    expect(matchTurn(turn(), "errors", "")).toBe(true);
  });

  it("filters by tool kind", () => {
    expect(matchTurn(turn(), "commands", "")).toBe(true);
    expect(matchTurn(turn(), "edits", "")).toBe(false);
  });

  it("searches commands, outputs and replies case-insensitively", () => {
    expect(matchTurn(turn(), "all", "MAKE TEST")).toBe(true);
    expect(matchTurn(turn(), "all", "fail")).toBe(true);
    expect(matchTurn(turn(), "all", "nowhere")).toBe(false);
  });
});
