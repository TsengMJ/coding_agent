package planmode

import (
	"fmt"
	"strings"

	"github.com/tsengmingchun/quill/middleware/todolist"
)

// PlanModePrompt is appended to the system prompt in plan mode.
const PlanModePrompt = "# Plan Mode\n\n" +
	"Plan mode is active. The user wants a reviewed plan before any change is made. You MUST NOT edit files, run commands or change the system in any other way; only read-only tools are available. This overrides any instruction to make changes or to keep working until the task is done.\n\n" +
	"## How to work\n\n" +
	"1. **Research.** Read the relevant code, grep for the symbols involved, look at existing tests, similar features and project conventions, and search the web when outside knowledge is needed. Find the exact places that must change and confirm your assumptions by reading the code instead of guessing.\n" +
	"2. **Clarify.** If a decision genuinely belongs to the user (an ambiguous requirement, a choice between approaches that change the result significantly), ask with `ask_user` — 1-2 questions at a time, early, before writing the plan. Don't ask what you can find out yourself.\n" +
	"3. **Plan.** When research is done, call `create_plan`.\n" +
	"4. **Stop.** After `create_plan`, reply with a short summary of the plan and end your turn. The user reviews it and either asks for changes or switches to build mode. When asked for changes, revise and call `create_plan` again (it overwrites the same plan).\n\n" +
	"## What makes a good plan\n\n" +
	"A todo list written while working may still contain open questions. A plan may not: resolve them now, while researching. Every step must rest on something you found, so that building it is mostly execution.\n\n" +
	"- Start with the goal and the relevant findings: the files, functions and data flow involved, citing paths (and short snippets where they help).\n" +
	"- Describe the approach step by step: which files change, what is added or modified, and why this approach over the alternatives you considered.\n" +
	"- Say how the result will be verified: the tests to add or run, the commands, the expected behavior.\n" +
	"- List risks and anything that could not be settled read-only, with how building will handle it.\n" +
	"- Use a mermaid diagram when a data flow or architecture is easier to see than to read.\n" +
	"- Keep it proportional: a small change gets a short plan. Be concise and specific, no filler.\n" +
	"- The todos are the ordered implementation steps. Each one is concrete and verifiable and names the files involved, and the list covers the whole plan, tests included."

// planSubagentsNote is added when read-only subagents stay available; %s
// lists them.
const planSubagentsNote = "\n\n## Subagents\n\n" +
	"`task` accepts only the read-only subagents %s in plan mode. Delegate broad code exploration and multi-step lookups to them, several at once when independent, and keep your own context for the plan."

const createPlanDescription = "Save the implementation plan for the user to review. Only available in plan mode. Call it once research is finished and open questions are resolved; call it again to revise (it overwrites the thread's plan). " +
	"`plan` is the markdown body: goal and findings with file paths, step-by-step approach, verification, risks. `todos` are the ordered, concrete implementation steps; they become the todo list when the user starts building. After calling it, summarize the plan briefly and end your turn."

func buildPrompt(p *Plan, todos []todolist.Todo) string {
	var b strings.Builder
	b.WriteString("# Approved Plan\n\n")
	b.WriteString("The user approved the plan below and asked you to build it. Implement it step by step, following the todo list and keeping it current with `write_todos` (use `merge: true` with the ids below). If the code turns out to differ from what the plan assumed, adapt, note the deviation in the todo's notes and tell the user.\n\n")
	fmt.Fprintf(&b, "## %s\n\n", p.Name)
	if p.Overview != "" {
		fmt.Fprintf(&b, "%s\n\n", p.Overview)
	}
	b.WriteString(strings.TrimSpace(p.Body))
	if len(todos) > 0 {
		b.WriteString("\n\n## Current todo list\n\n")
		for _, t := range todos {
			fmt.Fprintf(&b, "- [%s] `%s` %s\n", t.Status, t.ID, t.Content)
		}
	}
	return b.String()
}
