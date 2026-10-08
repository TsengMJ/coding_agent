package todolist

// ToolDescription extends langchain `WRITE_TODOS_TOOL_DESCRIPTION` with ids,
// notes, cancelled and merge, and asks for todos grounded in what the agent
// has already read.
const ToolDescription = "Create and manage a structured task list for the current work session. It keeps you on track during multi-step work and shows the user what you are doing and what is left.\n\n" +
	"## When to Use This Tool\n\n" +
	"1. Tasks that need 3 or more distinct steps, or touch several files or components\n" +
	"2. The user gives several things to do (numbered or comma-separated), or explicitly asks for a todo list\n" +
	"3. New requirements or follow-ups arrive while you are working — add them as items\n" +
	"4. You discover more work than expected — add it instead of keeping it in your head\n\n" +
	"## When NOT to Use This Tool\n\n" +
	"1. A single, straightforward change, or work done in 1-2 trivial steps\n" +
	"2. Purely conversational or informational requests\n\n" +
	"## Write Todos From Evidence\n\n" +
	"For any non-trivial task, look first: read the relevant files, grep for the symbols involved, check how tests are run. Then write the list. A list written before you have looked at the code can only be generic (\"inspect the code\", \"implement\", \"run tests\") and does not help you or the user.\n\n" +
	"Each item must be specific and actionable:\n" +
	"- Name the files, functions, endpoints or commands involved\n" +
	"- One concrete outcome per item; split items that hide several changes\n" +
	"- Say how the item will be verified (the test, command or output that proves it works), in the item or in its notes\n\n" +
	"Good: `Add a nil check for cfg.Timeout in client/http.go NewClient; cover it in client/http_test.go`\n" +
	"Bad: `Fix the bug`\n\n" +
	"Some things are still unknown when you write the list. Make them explicit investigation items (e.g. `Investigate: why TestRetry times out only under -race`) and record what you need to find out in notes. Once answered, replace the investigation item with the concrete work it revealed.\n\n" +
	"## Fields\n\n" +
	"- `id`: short, unique and stable across updates (e.g. `parse-flags`)\n" +
	"- `content`: what to do, as above\n" +
	"- `status`: pending | in_progress | completed | cancelled\n" +
	"- `notes` (optional): findings, open questions, the verification step\n" +
	"- `merge`: false (default) replaces the whole list; true updates items by id and appends new ones, so you can send only the items that changed\n\n" +
	"## Managing the List\n\n" +
	"`write_todos` never needs a turn of its own: send it in the same response as the other tool calls of that step (reads, edits, test runs). Call it at most once per response.\n\n" +
	"1. Mark an item in_progress when you start it, in the same response as its first tool calls. Normally one item is in_progress; several only when they are independent and done in parallel.\n" +
	"2. Mark an item completed as soon as it is done — typically in the response that starts the next item — rather than saving completions for the end.\n" +
	"3. ONLY mark completed when fully done: tests pass, no unresolved errors, nothing partial. If blocked, keep it in_progress and add an item describing what must be resolved.\n" +
	"4. Mark items that are no longer needed as cancelled instead of silently dropping them.\n" +
	"5. Revise freely as you learn: add follow-ups, split items, update notes. Don't rewrite completed items.\n" +
	"6. Unless every item is completed or cancelled, keep at least one item in_progress.\n\n" +
	"## When You Finish\n\n" +
	"`write_todos` tracks your work; it does not deliver the answer. Whatever the user asked for — computations, summaries, comparisons, data — must appear as text content in a message after your final `write_todos` call. Marking the last todo complete is not itself an answer to the user. Send that final `write_todos` in the same response as your last check (e.g. the final test run or diff), not on its own."

// SystemPrompt extends langchain `WRITE_TODOS_SYSTEM_PROMPT`.
const SystemPrompt = "## `write_todos`\n\n" +
	"You have access to the `write_todos` tool to plan and track multi-step work.\n" +
	"For non-trivial tasks, first explore enough to understand the code involved, then write a todo list whose items name concrete files, changes and verification steps. Items you are not yet sure about should be explicit investigation items.\n" +
	"Keep the list current: mark items in_progress before starting and completed as soon as they are done, add newly discovered work, cancel what is no longer needed. Use `merge: true` to update a few items by id.\n" +
	"For simple requests that take only a step or two, skip the list and just do the work.\n\n" +
	"## Important To-Do List Usage Notes to Remember\n\n" +
	"- Send `write_todos` in the same response as the tool calls for the work it describes; a response that only updates the list wastes a full model round trip. Call it at most once per response, since each call rewrites the same list.\n" +
	"- Don't be afraid to revise the To-Do list as you go. New information may reveal new tasks that need to be done, or old tasks that are irrelevant.\n\n" +
	"## Finishing a task\n\n" +
	"When you finish all work, write your final answer in the message AFTER your last `write_todos` call — not in the same turn as that call. Put that last `write_todos` together with your final check rather than in a turn of its own. Start the final message with the substantive content the user asked for — the data, computation, summary, or analysis. The user wants the result, not confirmation that the work is done."
