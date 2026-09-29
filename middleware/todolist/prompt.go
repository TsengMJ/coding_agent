package todolist

// ToolDescription is langchain `WRITE_TODOS_TOOL_DESCRIPTION`.
const ToolDescription = "Use this tool to create and manage a structured task list for your current work session. This helps you track progress and organize complex tasks.\n\n" +
	"Only use this tool if you think it will be helpful in staying organized. If the user's request is trivial and takes less than 3 steps, it is better to NOT use this tool and just do the task directly.\n\n" +
	"## When to Use This Tool\n\n" +
	"Use this tool in these scenarios:\n\n" +
	"1. Complex multi-step tasks - When a task requires 3 or more distinct steps or actions\n" +
	"2. Non-trivial and complex tasks - Tasks that require careful planning or multiple operations\n" +
	"3. User explicitly requests todo list - When the user directly asks you to use the todo list\n" +
	"4. User provides multiple tasks - When users provide a list of things to be done (numbered or comma-separated)\n" +
	"5. The plan may need future revisions or updates based on results from the first few steps\n\n" +
	"## How to Use This Tool\n\n" +
	"1. When you start working on a task - Mark it as in_progress BEFORE beginning work.\n" +
	"2. After completing a task - Mark it as completed and add any new follow-up tasks discovered during implementation.\n" +
	"3. You can also update future tasks, such as deleting them if they are no longer necessary, or adding new tasks that are necessary. Don't change previously completed tasks.\n" +
	"4. You can make several updates to the todo list at once. For example, when you complete a task, you can mark the next task you need to start as in_progress.\n\n" +
	"## When NOT to Use This Tool\n\n" +
	"It is important to skip using this tool when:\n" +
	"1. There is only a single, straightforward task\n" +
	"2. The task is trivial and tracking it provides no benefit\n" +
	"3. The task can be completed in less than 3 trivial steps\n" +
	"4. The task is purely conversational or informational\n\n" +
	"## Task States and Management\n\n" +
	"1. **Task States**: Use these states to track progress:\n" +
	"    - pending: Task not yet started\n" +
	"    - in_progress: Currently working on (you can have multiple tasks in_progress at a time if they are not related to each other and can be run in parallel)\n" +
	"    - completed: Task finished successfully\n\n" +
	"2. **Task Management**:\n" +
	"    - Update task status in real-time as you work\n" +
	"    - Mark tasks complete IMMEDIATELY after finishing (don't batch completions)\n" +
	"    - Complete current tasks before starting new ones\n" +
	"    - Remove tasks that are no longer relevant from the list entirely\n" +
	"    - IMPORTANT: When you write this todo list, you should mark your first task (or tasks) as in_progress immediately!.\n" +
	"    - IMPORTANT: Unless all tasks are completed, you should always have at least one task in_progress.\n\n" +
	"3. **Task Completion Requirements**:\n" +
	"    - ONLY mark a task as completed when you have FULLY accomplished it\n" +
	"    - If you encounter errors, blockers, or cannot finish, keep the task as in_progress\n" +
	"    - When blocked, create a new task describing what needs to be resolved\n" +
	"    - Never mark a task as completed if:\n" +
	"        - There are unresolved issues or errors\n" +
	"        - Work is partial or incomplete\n" +
	"        - You encountered blockers that prevent completion\n" +
	"        - You couldn't find necessary resources or dependencies\n" +
	"        - Quality standards haven't been met\n\n" +
	"4. **Task Breakdown**:\n" +
	"    - Create specific, actionable items\n" +
	"    - Break complex tasks into smaller, manageable steps\n" +
	"    - Use clear, descriptive task names\n\n" +
	"Being proactive with task management ensures you complete all requirements successfully\n" +
	"Remember: If you only need to make a few tool calls to complete a task, and it is clear what you need to do, it is better to just do the task directly and NOT call this tool at all.\n\n" +
	"## When You Finish\n\n" +
	"`write_todos` tracks your work; it does not deliver the answer. Whatever the user asked for — computations, summaries, comparisons, data — must appear as text content in a message after your final `write_todos` call. Marking the last todo complete is not itself an answer to the user."

// SystemPrompt is langchain `WRITE_TODOS_SYSTEM_PROMPT`.
const SystemPrompt = "## `write_todos`\n\n" +
	"You have access to the `write_todos` tool to help you manage and plan complex objectives.\n" +
	"Use this tool for complex objectives to ensure that you are tracking each necessary step.\n" +
	"This tool is very helpful for planning complex objectives, and for breaking down these larger complex objectives into smaller steps.\n\n" +
	"It is critical that you mark todos as completed as soon as you are done with a step. Do not batch up multiple steps before marking them as completed.\n" +
	"For simple objectives that only require a few steps, it is better to just complete the objective directly and NOT use this tool.\n" +
	"Writing todos takes time and tokens, use it when it is helpful for managing complex many-step problems! But not for simple few-step requests.\n\n" +
	"## Important To-Do List Usage Notes to Remember\n\n" +
	"- The `write_todos` tool should never be called multiple times in parallel.\n" +
	"- Don't be afraid to revise the To-Do list as you go. New information may reveal new tasks that need to be done, or old tasks that are irrelevant.\n\n" +
	"## Finishing a task\n\n" +
	"When you finish all work, write your final answer in the message AFTER your last `write_todos` call — not in the same turn as that call. Start the final message with the substantive content the user asked for — the data, computation, summary, or analysis. The user wants the result, not confirmation that the work is done."
