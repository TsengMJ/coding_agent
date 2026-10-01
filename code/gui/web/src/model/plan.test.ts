import { describe, expect, it } from "vitest";
import type { ToolCall } from "../types";
import { currentTodos, formatAnswers, planOf, planID, questionsOf, todosFromResult, todosOf } from "./plan";
import { emptyView, fromHistory, showBuild, startRun } from "./state";
import { summarize } from "./tools";

const call = (name: string, args: Record<string, unknown>): ToolCall => ({ id: "c", name, args });

describe("todos", () => {
  it("reads ids, notes and fills missing ids", () => {
    const todos = todosOf(
      call("write_todos", {
        todos: [
          { content: "a", status: "pending" },
          { id: "b", content: "b", status: "cancelled", notes: "dropped" },
        ],
      }),
    );
    expect(todos).toEqual([
      { id: "1", content: "a", status: "pending" },
      { id: "b", content: "b", status: "cancelled", notes: "dropped" },
    ]);
  });

  it("prefers the full list in the result of a merge call", () => {
    const c = call("write_todos", { merge: true, todos: [{ id: "a", content: "a", status: "completed" }] });
    const result = 'Updated todo list to [{"id":"a","content":"a","status":"completed"},{"id":"b","content":"b","status":"pending"}]';
    expect(currentTodos(c, result).map((t) => t.id)).toEqual(["a", "b"]);
    expect(currentTodos(c, "Error: boom").map((t) => t.id)).toEqual(["a"]);
    expect(todosFromResult("nope")).toBeNull();
    expect(summarize(c)).toBe("update 1 item");
  });
});

describe("plans", () => {
  const plan = call("create_plan", {
    name: "Add flag",
    overview: "Add -v.",
    plan: "## Approach",
    todos: [{ id: "flag", content: "Add the flag" }],
  });

  it("reads a create_plan call and its saved id", () => {
    expect(planOf(plan)).toEqual({
      name: "Add flag",
      overview: "Add -v.",
      body: "## Approach",
      todos: [{ id: "flag", content: "Add the flag", status: "pending" }],
    });
    expect(planID("Plan t-add-flag saved with 1 todos. End your turn")).toBe(
      "t-add-flag",
    );
    expect(summarize(plan)).toBe("Add flag · 1 todos");
  });

  it("offers Build only for an idle thread with a draft plan", () => {
    const draft = { name: "p", overview: "", body: "", todos: [], id: "t-p", status: "draft" };
    const v = fromHistory({ thread_id: "t", messages: [], plan: draft }, null);
    expect(showBuild(v)).toBe(true);
    expect(showBuild(startRun(v, "x"))).toBe(false);
    expect(showBuild({ ...v, plan: { ...draft, status: "building" } })).toBe(false);
    expect(showBuild(emptyView())).toBe(false);
  });
});

describe("ask_user", () => {
  const args = {
    questions: [
      { id: "db", prompt: "Which DB?", options: [{ id: "pg", label: "Postgres" }, { id: "lite", label: "SQLite" }] },
      { id: "x", prompt: "Extras?", allow_multiple: true, options: [{ id: "a", label: "A" }, { id: "b", label: "B" }] },
    ],
  };

  it("rejects malformed questions", () => {
    expect(questionsOf({ questions: [] })).toBeNull();
    expect(questionsOf({ questions: [{ id: "q", prompt: "?", options: [{ id: "a", label: "A" }] }] })).toBeNull();
    expect(questionsOf(args)).toHaveLength(2);
  });

  it("formats answers like askuser.FormatAnswers", () => {
    const qs = questionsOf(args)!;
    const text = formatAnswers(qs, [
      { question_id: "db", option_ids: ["lite"] },
      { question_id: "x", option_ids: ["a", "b"], other: "C too" },
    ]);
    expect(text).toBe("The user answered:\n1. Which DB?\n   -> SQLite\n2. Extras?\n   -> A; B; Other: C too");
    expect(formatAnswers(qs, [])).toContain("-> (no answer)");
  });
});
