// Package asyncsubagents launches and manages background subagent runs on a
// remote agent server (Python `AsyncSubAgentMiddleware`).
package asyncsubagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Tool names exposed by the middleware.
const (
	ToolStart  = "start_async_task"
	ToolCheck  = "check_async_task"
	ToolUpdate = "update_async_task"
	ToolCancel = "cancel_async_task"
	ToolList   = "list_async_tasks"
)

// TasksStateKey tracks launched tasks in graph state, keyed by task ID.
const TasksStateKey = "async_tasks"

// Run statuses that never change, so listing skips fetching them.
var terminalStatuses = []string{"cancelled", "success", "error", "timeout", "interrupted"}

// TaskToolDescription is the start_async_task description;
// {available_agents} lists the agents.
const TaskToolDescription = `Start an async subagent on a remote server. The subagent runs in the background and returns a task ID immediately.

Available async agent types:
{available_agents}

## Usage notes:
1. This tool launches a background task and returns immediately with a task ID. Report the task ID to the user and stop — do NOT immediately check status.
2. Use ` + "`check_async_task`" + ` only when the user asks for a status update or result.
3. Use ` + "`update_async_task`" + ` to send new instructions to a running task.
4. Multiple async subagents can run concurrently — launch several and let them run in the background.
5. The subagent runs on a remote server, so it has its own tools and capabilities.`

// AsyncSubAgent is a remote graph run in the background.
type AsyncSubAgent struct {
	Name        string
	Description string
	// GraphID is the graph name or assistant ID on the server.
	GraphID string
	// URL of the agent server; required (in-process transport is not
	// supported).
	URL     string
	Headers map[string]string
}

func (a AsyncSubAgent) SubAgentName() string { return a.Name }

// Task is a tracked background run; its ID is the remote thread ID.
type Task struct {
	TaskID        string `json:"task_id"`
	AgentName     string `json:"agent_name"`
	ThreadID      string `json:"thread_id"`
	RunID         string `json:"run_id"`
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at"`
	LastCheckedAt string `json:"last_checked_at"`
	LastUpdatedAt string `json:"last_updated_at"`
}

// Options configures the middleware beyond its agents.
type Options struct {
	// SystemPrompt, if set, is appended to the system message with the
	// list of agents.
	SystemPrompt string
	// HTTPClient defaults to one with a 30s timeout.
	HTTPClient *http.Client
	// Getenv reads API keys; nil uses os.Getenv.
	Getenv func(string) string
}

// Middleware provides the five async task tools.
type Middleware struct {
	agents  map[string]AsyncSubAgent
	names   []string
	prompt  string
	launch  string
	clients *clientCache
	now     func() time.Time
}

// New returns the middleware; at least one agent is required, and names
// must be unique.
func New(agents []AsyncSubAgent, opts Options) (*Middleware, error) {
	if len(agents) == 0 {
		return nil, errors.New("asyncsubagents.New: at least one async subagent is required")
	}
	getenv := opts.Getenv
	if getenv == nil {
		getenv = osGetenv
	}
	m := &Middleware{
		agents:  map[string]AsyncSubAgent{},
		clients: &clientCache{clients: map[string]*Client{}, http: opts.HTTPClient, getenv: getenv},
		now:     time.Now,
	}
	var lines []string
	for _, a := range agents {
		switch {
		case a.Name == "":
			return nil, errors.New("asyncsubagents.New: every async subagent needs a name")
		case m.agents[a.Name].Name != "":
			return nil, fmt.Errorf("asyncsubagents.New: duplicate async subagent name %q", a.Name)
		case a.GraphID == "":
			return nil, fmt.Errorf("asyncsubagents.New: async subagent %q needs a graph_id", a.Name)
		case a.URL == "":
			return nil, fmt.Errorf("asyncsubagents.New: async subagent %q needs a url; in-process transport is not supported", a.Name)
		}
		m.agents[a.Name] = a
		m.names = append(m.names, a.Name)
		lines = append(lines, fmt.Sprintf("- %s: %s", a.Name, a.Description))
	}
	available := strings.Join(lines, "\n")
	m.launch = strings.ReplaceAll(TaskToolDescription, "{available_agents}", available)
	if opts.SystemPrompt != "" {
		m.prompt = opts.SystemPrompt + "\n\nAvailable async subagent types:\n\n" + available
	}
	return m, nil
}

func (m *Middleware) Name() string { return "AsyncSubAgentMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{TasksStateKey: {Name: TasksStateKey, Reducer: mergeTasks}}
}

// mergeTasks adds or replaces tasks by ID.
func mergeTasks(old, update any) (any, error) {
	out := TasksFrom(old)
	if out == nil {
		out = map[string]Task{}
	}
	maps.Copy(out, TasksFrom(update))
	return out, nil
}

// TasksFrom reads a TasksStateKey value, also in its JSON checkpoint form.
func TasksFrom(v any) map[string]Task {
	switch t := v.(type) {
	case nil:
		return nil
	case map[string]Task:
		return maps.Clone(t)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]Task
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	if m.prompt != "" {
		req.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, m.prompt)
	}
	return next(ctx, req)
}

func taskIDSchema() map[string]any {
	return map[string]any{"type": "string", "description": "The exact task_id string returned by start_async_task. Pass it verbatim."}
}

func (m *Middleware) Tools() []tool.Tool {
	obj := func(props map[string]any, required ...string) tool.Schema {
		s := tool.Schema{"type": "object", "properties": props, "additionalProperties": false}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	return []tool.Tool{
		&tool.Func{ToolName: ToolStart, ToolDescription: m.launch, Fn: m.start, ToolSchema: obj(map[string]any{
			"description":   map[string]any{"type": "string", "description": "A detailed description of the task for the async subagent to perform."},
			"subagent_type": map[string]any{"type": "string", "description": "The type of async subagent to use. Must be one of the available types listed in the tool description."},
		}, "description", "subagent_type")},
		&tool.Func{ToolName: ToolCheck, Fn: m.check, ToolSchema: obj(map[string]any{"task_id": taskIDSchema()}, "task_id"),
			ToolDescription: "Check the status of an async subagent task. Returns the current status and, if complete, the result. " +
				"Statuses shown earlier in the conversation are always stale, so call this to get the current status rather than reporting a status from a previous tool result."},
		&tool.Func{ToolName: ToolUpdate, Fn: m.update, ToolSchema: obj(map[string]any{
			"task_id": taskIDSchema(),
			"message": map[string]any{"type": "string", "description": "Follow-up instructions or context to send to the subagent."},
		}, "task_id", "message"),
			ToolDescription: "Send updated instructions to an async subagent. Interrupts the current run and starts a new one on the same thread, so the subagent sees the full conversation history plus your new message. The task_id remains the same."},
		&tool.Func{ToolName: ToolCancel, Fn: m.cancel, ToolSchema: obj(map[string]any{"task_id": taskIDSchema()}, "task_id"),
			ToolDescription: "Cancel a running async subagent task. Use this to stop a task that is no longer needed."},
		&tool.Func{ToolName: ToolList, Fn: m.list, ToolSchema: obj(map[string]any{
			"status_filter": map[string]any{"type": "string", "enum": []string{"running", "success", "error", "cancelled", "all"},
				"description": "Filter tasks by status. One of: 'running', 'success', 'error', 'cancelled', 'all'. Defaults to 'all'."},
		}),
			ToolDescription: "List tracked async subagent tasks with their current live statuses. By default shows all tasks. Use `status_filter` to narrow by status " +
				"(e.g. 'running', 'success', 'error', 'cancelled'). Use `check_async_task` to get the full result of a specific completed task. " +
				"Statuses shown earlier in the conversation are always stale, so call this to read current statuses rather than reporting one from a previous tool result."},
	}
}

func (m *Middleware) stamp() string { return m.now().UTC().Format("2006-01-02T15:04:05Z") }

func (m *Middleware) start(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	description, _ := args["description"].(string)
	name, _ := args["subagent_type"].(string)
	a, ok := m.agents[name]
	if !ok {
		quoted := make([]string, len(m.names))
		for i, n := range m.names {
			quoted[i] = "`" + n + "`"
		}
		return errorText(fmt.Sprintf("Unknown async subagent type `%s`. Available types: %s", name, strings.Join(quoted, ", "))), nil
	}
	client := m.clients.get(a)
	thread, err := client.CreateThread(ctx)
	if err != nil {
		return errorText(fmt.Sprintf("Failed to launch async subagent '%s': %v", name, err)), nil
	}
	run, err := client.CreateRun(ctx, thread, a.GraphID, userInput(description), "")
	if err != nil {
		return errorText(fmt.Sprintf("Failed to launch async subagent '%s': %v", name, err)), nil
	}
	now := m.stamp()
	task := Task{TaskID: thread, AgentName: name, ThreadID: thread, RunID: run.RunID, Status: "running", CreatedAt: now, LastCheckedAt: now, LastUpdatedAt: now}
	return tracked(rt, ToolStart, "Launched async subagent. task_id: "+thread, task), nil
}

func (m *Middleware) check(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	task, a, msg := m.resolve(rt, args)
	if msg != "" {
		return errorText(msg), nil
	}
	client := m.clients.get(a)
	run, err := client.GetRun(ctx, task.ThreadID, task.RunID)
	if err != nil {
		return errorText(fmt.Sprintf("Failed to get run status: %v", err)), nil
	}
	result := map[string]any{"status": run.Status, "thread_id": task.ThreadID}
	switch run.Status {
	case "success":
		values, err := client.ThreadValues(ctx, task.ThreadID)
		if err != nil {
			values = nil
		}
		result["result"] = lastMessageText(values)
	case "error":
		result["error"] = "The async subagent encountered an error."
		if run.Error != nil {
			result["error"] = fmt.Sprint(run.Error)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	now := m.stamp()
	if task.Status != run.Status {
		task.LastUpdatedAt = now
	}
	task.Status, task.LastCheckedAt = run.Status, now
	return tracked(rt, ToolCheck, string(raw), task), nil
}

func (m *Middleware) update(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	task, a, msg := m.resolve(rt, args)
	if msg != "" {
		return errorText(msg), nil
	}
	message, _ := args["message"].(string)
	run, err := m.clients.get(a).CreateRun(ctx, task.ThreadID, a.GraphID, userInput(message), "interrupt")
	if err != nil {
		return errorText(fmt.Sprintf("Failed to update async subagent: %v", err)), nil
	}
	task.RunID, task.Status, task.LastUpdatedAt = run.RunID, "running", m.stamp()
	return tracked(rt, ToolUpdate, "Updated async subagent. task_id: "+task.TaskID, task), nil
}

func (m *Middleware) cancel(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	task, a, msg := m.resolve(rt, args)
	if msg != "" {
		return errorText(msg), nil
	}
	if err := m.clients.get(a).CancelRun(ctx, task.ThreadID, task.RunID); err != nil {
		return errorText(fmt.Sprintf("Failed to cancel run: %v", err)), nil
	}
	now := m.stamp()
	task.Status, task.LastCheckedAt, task.LastUpdatedAt = "cancelled", now, now
	return tracked(rt, ToolCancel, "Cancelled async subagent task: "+task.TaskID, task), nil
}

func (m *Middleware) list(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	filter, _ := args["status_filter"].(string)
	var tasks []Task
	for _, t := range stateTasks(rt) {
		if filter == "" || filter == "all" || t.Status == filter {
			tasks = append(tasks, t)
		}
	}
	if len(tasks) == 0 {
		return textResult("No async subagent tasks tracked."), nil
	}
	slices.SortFunc(tasks, func(a, b Task) int { return strings.Compare(a.CreatedAt+a.TaskID, b.CreatedAt+b.TaskID) })
	now := m.stamp()
	updated := map[string]Task{}
	entries := make([]string, len(tasks))
	for i, t := range tasks {
		status := m.liveStatus(ctx, t)
		entries[i] = fmt.Sprintf("- task_id: %s  agent: %s  status: %s", t.TaskID, t.AgentName, status)
		if status != t.Status {
			t.LastUpdatedAt = now
		}
		t.Status, t.LastCheckedAt = status, now
		updated[t.TaskID] = t
	}
	text := fmt.Sprintf("%d tracked task(s):\n%s", len(entries), strings.Join(entries, "\n"))
	return commandResult(rt, ToolList, text, updated), nil
}

// liveStatus asks the server for a task's status, keeping the stored one
// when it is final or the server cannot be reached.
func (m *Middleware) liveStatus(ctx context.Context, t Task) string {
	a, ok := m.agents[t.AgentName]
	if !ok || slices.Contains(terminalStatuses, t.Status) {
		return t.Status
	}
	run, err := m.clients.get(a).GetRun(ctx, t.ThreadID, t.RunID)
	if err != nil || run.Status == "" {
		return t.Status
	}
	return run.Status
}

// resolve finds the tracked task named by args["task_id"] and its agent,
// or returns a message for the model.
func (m *Middleware) resolve(rt *tool.Runtime, args map[string]any) (Task, AsyncSubAgent, string) {
	id, _ := args["task_id"].(string)
	task, ok := stateTasks(rt)[strings.TrimSpace(id)]
	if !ok {
		return Task{}, AsyncSubAgent{}, fmt.Sprintf("No tracked task found for task_id: %q", id)
	}
	a, ok := m.agents[task.AgentName]
	if !ok {
		return Task{}, AsyncSubAgent{}, fmt.Sprintf("Task %s belongs to async subagent %q, which is no longer configured", id, task.AgentName)
	}
	return task, a, ""
}

func stateTasks(rt *tool.Runtime) map[string]Task {
	if rt == nil || rt.State == nil {
		return nil
	}
	return TasksFrom(rt.State.Values[TasksStateKey])
}

func userInput(text string) map[string]any {
	return map[string]any{"messages": []any{map[string]any{"role": "user", "content": text}}}
}

// lastMessageText is the content of the thread's last message.
func lastMessageText(values map[string]any) string {
	msgs, _ := values["messages"].([]any)
	if len(msgs) == 0 {
		return "(completed with no output messages)"
	}
	last, ok := msgs[len(msgs)-1].(map[string]any)
	if !ok {
		return fmt.Sprint(msgs[len(msgs)-1])
	}
	switch c := last["content"].(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, block := range c {
			if b, ok := block.(map[string]any); ok && b["type"] == "text" {
				if s, ok := b["text"].(string); ok {
					parts = append(parts, s)
				}
			}
		}
		return strings.Join(parts, "")
	case nil:
		return ""
	default:
		raw, _ := json.Marshal(c)
		return string(raw)
	}
}

func tracked(rt *tool.Runtime, name, text string, t Task) *tool.Result {
	return commandResult(rt, name, text, map[string]Task{t.TaskID: t})
}

func commandResult(rt *tool.Runtime, name, text string, tasks map[string]Task) *tool.Result {
	var callID string
	if rt != nil {
		callID = rt.ToolCallID
	}
	return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{
		Messages: []llm.Message{llm.NewToolMessage(callID, name, text)},
		Values:   map[string]any{TasksStateKey: tasks},
	}}}
}

func textResult(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}
}

func errorText(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}, IsError: true}
}
