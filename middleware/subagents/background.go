package subagents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// TaskOutputToolName reads or waits for a background task's result.
const TaskOutputToolName = "task_output"

// Background task statuses, as shown in notifications and task_output.
const (
	TaskRunning   = "running"
	TaskCompleted = "completed"
	TaskFailed    = "failed"
	TaskCancelled = "cancelled"
)

// task_output waits this long by default, and at most maxOutputWait.
const (
	defaultOutputWait = 30 * time.Second
	maxOutputWait     = 10 * time.Minute
)

const backgroundTaskNote = `
- Set run_in_background to true to start the agent and continue with other work. You get a task id at once and a <task-notification> message with the report when it finishes; call task_output only when you cannot proceed without the result. Background agents cannot ask for approval, so give them only work that needs none.`

// bgTask is one background subagent run. Fields after done are written
// before done is closed.
type bgTask struct {
	id, thread, subagentType string
	done                     chan struct{}
	status, report           string
	usage                    []ModelUsage
}

// registry tracks background tasks in process memory; they do not survive a
// restart. changed is closed and replaced whenever a task finishes.
type registry struct {
	mu      sync.Mutex
	tasks   map[string]*bgTask
	changed chan struct{}
}

func newRegistry() *registry {
	return &registry{tasks: map[string]*bgTask{}, changed: make(chan struct{})}
}

func (r *registry) start(t *bgTask) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[t.id] = t
}

func (r *registry) finish(t *bgTask, status, report string, usage []ModelUsage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.status, t.report, t.usage = status, report, usage
	close(t.done)
	close(r.changed)
	r.changed = make(chan struct{})
}

// take removes and returns the thread's finished tasks.
func (r *registry) take(thread string) []*bgTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*bgTask
	for id, t := range r.tasks {
		if t.thread == thread && isDone(t) {
			out = append(out, t)
			delete(r.tasks, id)
		}
	}
	return out
}

// pending reports whether the thread has undelivered tasks, whether any of
// them has finished, and the channel that signals the next finish.
func (r *registry) pending(thread string) (any, finished bool, changed <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.thread == thread {
			any = true
			finished = finished || isDone(t)
		}
	}
	return any, finished, r.changed
}

func (r *registry) get(thread, id string) *bgTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tasks[id]; t != nil && t.thread == thread {
		return t
	}
	return nil
}

// deliver removes a finished task whose result was handed to the model.
func (r *registry) deliver(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tasks, id)
}

func isDone(t *bgTask) bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// startBackground runs the subagent in a goroutine under the run's context,
// so cancelling the run cancels it. Its events are not forwarded: the parent
// stream may end before it does.
func (m *Middleware) startBackground(ctx context.Context, a agent.Agent, in agent.Input, cfg runtime.RunConfig, rt *tool.Runtime, subagentType, model string) *tool.Result {
	t := &bgTask{id: rt.ToolCallID, thread: rt.Config.ThreadID, subagentType: subagentType, done: make(chan struct{})}
	m.bg.start(t)
	go func() {
		st, err := a.Invoke(ctx, in, cfg)
		usage := usageOf(st, model)
		switch {
		case ctx.Err() != nil:
			m.bg.finish(t, TaskCancelled, "The run was cancelled before the task finished.", usage)
		case err != nil:
			m.bg.finish(t, TaskFailed, err.Error(), usage)
		default:
			report, rerr := reportText(st)
			if rerr != nil {
				m.bg.finish(t, TaskFailed, rerr.Error(), usage)
				return
			}
			m.bg.finish(t, TaskCompleted, report, usage)
		}
	}()
	return textResult(fmt.Sprintf("Started background task %s (%s). Continue with other work; a <task-notification> with its report arrives when it finishes. Use %s only if you must wait for it.",
		t.id, subagentType, TaskOutputToolName))
}

func notification(t *bgTask) string {
	return fmt.Sprintf("<task-notification>\n<task-id>%s</task-id>\n<subagent-type>%s</subagent-type>\n<status>%s</status>\n<result>\n%s\n</result>\n</task-notification>",
		t.id, t.subagentType, t.status, t.report)
}

// BeforeModel adds one notification message per finished background task;
// as messages they reach the checkpoint.
func (m *Middleware) BeforeModel(_ context.Context, _ *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	if m.bg == nil {
		return nil, nil
	}
	done := m.bg.take(rt.Config.ThreadID)
	if len(done) == 0 {
		return nil, nil
	}
	msgs := make([]llm.Message, len(done))
	for i, t := range done {
		msgs[i] = llm.NewUserMessage(notification(t))
		msgs[i].Metadata = withUsage(map[string]any{"task_notification": t.id}, t.usage)
	}
	return &runtime.Update{Messages: msgs}, nil
}

// AfterAgent keeps the run open while background tasks are running: it waits
// for the next one to finish and returns to the model, which then sees the
// notification.
func (m *Middleware) AfterAgent(ctx context.Context, _ *runtime.State, rt *agent.Runtime) (*runtime.Command, error) {
	if m.bg == nil {
		return nil, nil
	}
	for {
		any, finished, changed := m.bg.pending(rt.Config.ThreadID)
		if !any {
			return nil, nil
		}
		if finished {
			return &runtime.Command{Goto: agent.GotoModel}, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (m *Middleware) taskOutputTool() tool.Tool {
	return &tool.Func{
		ToolName: TaskOutputToolName,
		ToolDescription: "Get the report of a background task started with `task` and run_in_background. Waits until the task finishes or the timeout passes. " +
			"You are notified automatically when a task finishes, so call this only when you cannot continue without the result.",
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The id returned when the task started."},
				"timeout": map[string]any{"type": "number", "description": fmt.Sprintf("Seconds to wait; 0 checks without waiting. Default %d, max %d.", int(defaultOutputWait.Seconds()), int(maxOutputWait.Seconds()))},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		Fn: m.taskOutput,
	}
}

func (m *Middleware) taskOutput(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	id, _ := args["task_id"].(string)
	var thread string
	if rt != nil {
		thread = rt.Config.ThreadID
	}
	t := m.bg.get(thread, id)
	if t == nil {
		return nil, fmt.Errorf("no background task %q is pending; finished tasks are reported once, in a <task-notification> or an earlier %s result", id, TaskOutputToolName)
	}
	wait := defaultOutputWait
	if v, ok := args["timeout"].(float64); ok {
		wait = min(max(time.Duration(v*float64(time.Second)), 0), maxOutputWait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-t.done:
	case <-timer.C:
		return textResult(fmt.Sprintf("Task %s is still running.", id)), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m.bg.deliver(id)
	text := fmt.Sprintf("Task %s %s.\n\n%s", id, t.status, t.report)
	if len(t.usage) == 0 || rt == nil || rt.ToolCallID == "" {
		return textResult(text), nil
	}
	msg := llm.NewToolMessage(rt.ToolCallID, TaskOutputToolName, text)
	msg.Metadata = withUsage(msg.Metadata, t.usage)
	return &tool.Result{Command: &runtime.Command{Update: &runtime.Update{Messages: []llm.Message{msg}}}}, nil
}

// reportText is the subagent's structured response as JSON, else its last
// non-empty assistant message.
func reportText(st *runtime.State) (string, error) {
	if st == nil {
		return "", errors.New("subagent returned no state")
	}
	if st.StructuredResponse != nil {
		raw, err := json.Marshal(st.StructuredResponse)
		if err != nil {
			return "", fmt.Errorf("encode subagent structured response: %w", err)
		}
		return string(raw), nil
	}
	for i := len(st.Messages) - 1; i >= 0; i-- {
		msg := st.Messages[i]
		if msg.Role != llm.RoleAssistant {
			continue
		}
		if text := strings.TrimRight(msg.Text(), " \t\r\n"); text != "" {
			return text, nil
		}
	}
	return "", nil
}

// Notification is a parsed <task-notification> message.
type Notification struct {
	TaskID, SubagentType, Status, Result string
}

// ParseNotification reads a message written by BeforeModel.
func ParseNotification(text string) (Notification, bool) {
	body, ok := strings.CutPrefix(text, "<task-notification>\n")
	if !ok {
		return Notification{}, false
	}
	var n Notification
	field := func(tag string) bool {
		open, closing := "<"+tag+">", "</"+tag+">\n"
		rest, ok := strings.CutPrefix(body, open)
		if !ok {
			return false
		}
		value, after, ok := strings.Cut(rest, closing)
		if !ok {
			return false
		}
		body = after
		switch tag {
		case "task-id":
			n.TaskID = value
		case "subagent-type":
			n.SubagentType = value
		case "status":
			n.Status = value
		}
		return true
	}
	if !field("task-id") || !field("subagent-type") || !field("status") {
		return Notification{}, false
	}
	result, ok := strings.CutPrefix(body, "<result>\n")
	if !ok {
		return Notification{}, false
	}
	result, ok = strings.CutSuffix(result, "\n</result>\n</task-notification>")
	if !ok {
		return Notification{}, false
	}
	n.Result = result
	return n, true
}
