// Package planmode adds a read-only planning mode: the model researches with
// read-only tools, may ask the user questions, and saves a plan with a todo
// checklist through create_plan. A later run with BuildKey set seeds the todo
// list from the plan and keeps the approved plan in the system prompt.
//
// The mode is chosen per run through RunConfig.Configurable, so one agent
// serves both modes.
package planmode

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/subagents"
	"github.com/tsengmingchun/quill/apps/agent/middleware/todolist"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	// ModeKey is the RunConfig.Configurable key holding the run's mode.
	ModeKey = "agent_mode"
	// ModePlan selects plan mode; any other value is the normal agent mode.
	ModePlan = "plan"
	// BuildKey, when true in RunConfig.Configurable, starts implementing the
	// thread's current plan.
	BuildKey = "build_plan"
	// PlanStateKey stores the thread's current Plan.
	PlanStateKey = "plan"
	// ToolName saves the plan.
	ToolName = "create_plan"
)

// Plan statuses.
const (
	StatusDraft    = "draft"
	StatusBuilding = "building"
)

// Plan is the latest plan of a thread.
type Plan struct {
	Name     string `json:"name"`
	Overview string `json:"overview"`
	// Body is the plan's markdown, without the checklist.
	Body  string          `json:"body"`
	Todos []todolist.Todo `json:"todos"`
	// ID names the plan in the Store; a revised plan keeps its ID.
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Store keeps saved plans so they outlive the run and the process.
type Store interface {
	// Save creates or replaces the plan with p.ID.
	Save(ctx context.Context, threadID string, p Plan) error
	// Get returns the plan with id, or nil when there is none.
	Get(ctx context.Context, id string) (*Plan, error)
}

// MemoryStore keeps plans in process memory (tests).
type MemoryStore struct {
	mu    sync.Mutex
	plans map[string]Plan
}

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{plans: map[string]Plan{}} }

func (s *MemoryStore) Save(_ context.Context, _ string, p Plan) error {
	if p.ID == "" {
		return errors.New("planmode: plan has no id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p.Todos = slices.Clone(p.Todos)
	s.plans[p.ID] = p
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.plans[id]
	if !ok {
		return nil, nil
	}
	p.Todos = slices.Clone(p.Todos)
	return &p, nil
}

// DefaultReadOnlyTools are the tools plan mode keeps.
var DefaultReadOnlyTools = []string{
	"ls", "read_file", "glob", "grep",
	"web_search", "fetch_url", "resolve_library_id", "query_docs",
	"git_status", "git_diff",
	"ask_user", ToolName,
}

// Options configures the middleware.
type Options struct {
	// Store saves plans; required.
	Store Store
	// ReadOnlyTools defaults to DefaultReadOnlyTools.
	ReadOnlyTools []string
	// ReadOnlySubagents may still be started with `task` (and read with
	// `task_output`) in plan mode; empty hides both.
	ReadOnlySubagents []string
}

// Middleware implements plan mode.
type Middleware struct {
	store     Store
	readOnly  []string
	subagents []string
}

// New returns the middleware.
func New(opts Options) (*Middleware, error) {
	if opts.Store == nil {
		return nil, errors.New("planmode.New: Store is required")
	}
	ro := opts.ReadOnlyTools
	if len(ro) == 0 {
		ro = DefaultReadOnlyTools
	}
	if len(opts.ReadOnlySubagents) > 0 {
		ro = append(slices.Clone(ro), subagents.TaskToolName, subagents.TaskOutputToolName)
	}
	return &Middleware{store: opts.Store, readOnly: ro, subagents: slices.Clone(opts.ReadOnlySubagents)}, nil
}

func (m *Middleware) Name() string { return "PlanModeMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{PlanStateKey: {Name: PlanStateKey, OmitFromInput: true}}
}

// IsPlanMode reports whether cfg selects plan mode.
func IsPlanMode(cfg runtime.RunConfig) bool {
	mode, _ := cfg.Configurable[ModeKey].(string)
	return mode == ModePlan
}

func isPlanMode(rt *agent.Runtime) bool { return rt != nil && IsPlanMode(rt.Config) }

// WrapModelCall narrows the tools to the read-only set in plan mode and adds
// the plan-mode prompt; otherwise it hides create_plan and, while a plan is
// being built, adds the approved plan and the current todos.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	updated := *req
	if isPlanMode(req.Runtime) {
		updated.Tools = slices.DeleteFunc(slices.Clone(req.Tools), func(t tool.Tool) bool { return !slices.Contains(m.readOnly, t.Name()) })
		prompt := PlanModePrompt
		if len(m.subagents) > 0 {
			prompt += fmt.Sprintf(planSubagentsNote, quoteNames(m.subagents))
		}
		updated.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, prompt)
		return next(ctx, &updated)
	}
	updated.Tools = slices.DeleteFunc(slices.Clone(req.Tools), func(t tool.Tool) bool { return t.Name() == ToolName })
	if p := PlanOf(req.State); p != nil && p.Status == StatusBuilding {
		updated.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, buildPrompt(p, currentTodos(req.State)))
	}
	return next(ctx, &updated)
}

// WrapToolCall refuses any non-read-only tool in plan mode, in case the
// model calls a tool it was not offered.
func (m *Middleware) WrapToolCall(ctx context.Context, req *agent.ToolCallRequest, next agent.ToolHandler) (*tool.Result, error) {
	if isPlanMode(req.Runtime) && !slices.Contains(m.readOnly, req.ToolCall.Name) {
		return &tool.Result{
			Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("Error: `%s` is not available in plan mode, which is read-only. Research with read-only tools and save the plan with `%s`; the user switches to build mode to make changes.", req.ToolCall.Name, ToolName))},
			IsError: true,
		}, nil
	}
	if isPlanMode(req.Runtime) && req.ToolCall.Name == subagents.TaskToolName {
		if name, _ := req.ToolCall.Args["subagent_type"].(string); !slices.Contains(m.subagents, name) {
			return &tool.Result{
				Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf("Error: subagent `%s` may change files, so it is not available in plan mode. In plan mode `task` accepts only %s.", name, quoteNames(m.subagents)))},
				IsError: true,
			}, nil
		}
	}
	return next(ctx, req)
}

func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "`" + n + "`"
	}
	return strings.Join(quoted, ", ")
}

// BeforeAgent starts building: the plan's todos become the todo list (the
// first in_progress) and the plan is marked as building.
func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	if rt == nil {
		return nil, nil
	}
	if build, _ := rt.Config.Configurable[BuildKey].(bool); !build {
		return nil, nil
	}
	p := PlanOf(state)
	if p == nil {
		return nil, errors.New("planmode: this thread has no plan to build; create one in plan mode first")
	}
	todos := make([]todolist.Todo, len(p.Todos))
	for i, t := range p.Todos {
		t.Status = todolist.StatusPending
		if i == 0 {
			t.Status = todolist.StatusInProgress
		}
		todos[i] = t
	}
	built := *p
	built.Status = StatusBuilding
	if built.ID != "" {
		if err := m.store.Save(ctx, rt.Config.ThreadID, built); err != nil {
			return nil, fmt.Errorf("planmode: save plan: %w", err)
		}
	}
	return &runtime.Update{Values: map[string]any{todolist.TodosStateKey: todos, PlanStateKey: built}}, nil
}

// PlanOf returns the thread's plan, or nil.
func PlanOf(state *runtime.State) *Plan {
	if state == nil {
		return nil
	}
	p, err := ParsePlan(state.Values[PlanStateKey])
	if err != nil {
		return nil
	}
	return p
}

func currentTodos(state *runtime.State) []todolist.Todo {
	if state == nil {
		return nil
	}
	v, ok := state.Values[todolist.TodosStateKey]
	if !ok || v == nil {
		return nil
	}
	todos, err := todolist.ParseTodos(v)
	if err != nil {
		return nil
	}
	return todos
}

// RenderMarkdown is the plan as markdown: title, overview, body and a
// checklist.
func RenderMarkdown(p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", p.Name)
	if p.Overview != "" {
		fmt.Fprintf(&b, "%s\n\n", p.Overview)
	}
	b.WriteString(strings.TrimSpace(p.Body))
	b.WriteString("\n\n## Todos\n\n")
	for _, t := range p.Todos {
		mark := " "
		if t.Status == todolist.StatusCompleted {
			mark = "x"
		}
		fmt.Fprintf(&b, "- [%s] %s (`%s`)\n", mark, t.Content, t.ID)
	}
	return b.String()
}

// slug turns a name into a short id part.
func slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "plan"
	}
	return s
}
