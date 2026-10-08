// Package subagents delegates work to ephemeral subagents through the `task`
// tool (Python `SubAgentMiddleware`, `SubAgent`, `CompiledSubAgent`).
package subagents

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/middleware/permissions"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// TaskToolName is the delegation tool exposed to the parent agent.
const TaskToolName = "task"

// Defaults for the auto-added general-purpose subagent.
const (
	GeneralPurposeName        = "general-purpose"
	GeneralPurposeDescription = "General-purpose agent for researching complex questions, searching for files and content, and executing multi-step tasks. When you are searching for a keyword or file and are not confident that you will find the right match in the first few tries use this agent to perform the search for you. This agent has access to all tools as the main agent."
	DefaultSubagentPrompt     = "In order to complete the objective that the user asks of you, you have access to a number of standard tools.\n\nThe calling agent only sees your final assistant message, not your intermediate work, tool results, or status tracking. Ensure your final\nresponse contains the complete answer."
)

// TaskToolDescription is the default `task` description; {available_agents}
// is replaced with one "- name: description" line per subagent.
const TaskToolDescription = `Launch an ephemeral subagent to handle a complex, multi-step task.

Available agent types and the tools they have access to:
{available_agents}

Specify subagent_type to select the agent. Usage notes:
- Launch multiple agents concurrently when their tasks are independent, using a single message with multiple tool calls.
- Each invocation is stateless by default: the agent sees only the prompt you give it and returns a single final report. Put full detail in the prompt and state exactly what it should return — unless an agent type below says it inherits your conversation instead.
- The agent's report is not shown to the user; relay a summary yourself.
- Tell the agent whether to create content, analyze, or only research, since it can't necessarily see the user's intent unless it inherits your conversation, as noted per agent type below.
- If an agent's description says to use it proactively, do so without waiting to be asked.
- When only general-purpose is available, use it for any complex, context-heavy task; it has the same capabilities as the main agent.`

// ExcludedStateKeys are never passed to a subagent nor copied back from it:
// messages are handled explicitly, and the rest have no meaning across agents.
var ExcludedStateKeys = []string{"messages", "todos", "structured_response", "skills_metadata", ForkedContextKey}

// Mode controls how much parent context a subagent sees.
type Mode string

const (
	// ModeIsolated passes only the task description (default).
	ModeIsolated Mode = "isolated"
	// ModeFork continues the parent conversation and rebuilds its system prompt (experimental).
	ModeFork Mode = "fork"
	// modeHandoff is the legacy alias of ModeIsolated.
	modeHandoff Mode = "handoff"
)

// Spec is implemented by every subagent kind accepted by CreateAgent
// (SubAgent, CompiledSubAgent and asyncsubagents.AsyncSubAgent).
type Spec interface {
	SubAgentName() string
}

// SubAgent is a declarative synchronous subagent.
//
// For Tools and Permissions, nil means "inherit from the parent" and a
// non-nil empty slice means "none".
type SubAgent struct {
	Name         string
	Description  string
	SystemPrompt string
	Mode         Mode
	Tools        []tool.Tool
	// Model overrides the parent model; ModelSpec ("provider:model") is used when Model is nil.
	Model       llm.ChatModel
	ModelSpec   string
	Middleware  []agent.Middleware
	InterruptOn map[string]hitl.InterruptOnConfig
	// Skills are skill source paths; like Permissions, they are applied by
	// quill.CreateAgent and rejected when used directly here.
	Skills []string
	// Permissions are applied by quill.CreateAgent to the
	// subagent's filesystem middleware; used directly here they are rejected.
	Permissions    []permissions.FilesystemPermission
	ResponseFormat agent.ResponseFormat
	// ReadOnly promises the subagent cannot change the workspace, so its
	// task calls may run alongside the parent's other reads.
	ReadOnly bool
}

func (s SubAgent) SubAgentName() string { return s.Name }

// CompiledSubAgent wraps a prebuilt agent. Its result is the structured
// response if any, else the last non-empty assistant message.
type CompiledSubAgent struct {
	Name        string
	Description string
	Runnable    agent.Agent
	Mode        Mode
	// ReadOnly is as for SubAgent.
	ReadOnly bool
}

func (s CompiledSubAgent) SubAgentName() string { return s.Name }

// Options configures the middleware.
type Options struct {
	Backend   backend.Backend
	SubAgents []Spec
	// TaskDescription overrides the task tool description; {available_agents}
	// is replaced when present.
	TaskDescription string
	// SystemPrompt, if set, is appended to the parent's system message
	// together with the list of subagents.
	SystemPrompt string
	// PrivateStateKeys are excluded from state passed to isolated subagents.
	PrivateStateKeys []string
	StateSchema      runtime.Schema
	// Background lets `task` run subagents in the background
	// (run_in_background) and adds the task_output tool.
	Background bool
}

// Middleware provides the `task` tool.
type Middleware struct {
	opts   Options
	agents map[string]agent.Agent
	// models names each subagent's model for its untagged replies.
	models      map[string]string
	specs       map[string]Spec
	names       []string
	description string
	prompt      string
	bg          *registry
}

// New returns a subagent middleware. Declarative specs must already be fully
// resolved (model, tools, middleware, prompt) by the caller, as
// CreateAgent does: nil Tools means no tools here.
func New(opts Options) (*Middleware, error) {
	if len(opts.SubAgents) == 0 {
		return nil, errors.New("subagents.New: at least one subagent is required")
	}
	if err := ValidateSpecs(opts.SubAgents); err != nil {
		return nil, err
	}
	m := &Middleware{opts: opts, agents: map[string]agent.Agent{}, models: map[string]string{}, specs: map[string]Spec{}}
	var lines []string
	for _, spec := range opts.SubAgents {
		note := ""
		if isFork(spec) {
			note = forkedToolNote
		}
		lines = append(lines, fmt.Sprintf("- %s: %s%s", spec.SubAgentName(), specDescription(spec), note))
	}
	available := strings.Join(lines, "\n")
	m.description = strings.ReplaceAll(TaskToolDescription, "{available_agents}", available)
	if opts.TaskDescription != "" {
		m.description = strings.ReplaceAll(opts.TaskDescription, "{available_agents}", available)
	}
	if opts.Background {
		m.bg = newRegistry()
		m.description += backgroundTaskNote
	}
	if opts.SystemPrompt != "" {
		m.prompt = opts.SystemPrompt + "\n\nAvailable subagent types:\n\n" + available
	}
	for _, spec := range opts.SubAgents {
		if s, ok := spec.(SubAgent); ok && s.Mode == ModeFork {
			s.Middleware = withForkTaskTool(s.Middleware, m.taskTool())
			spec = s
		}
		a, err := compile(spec, opts.StateSchema)
		if err != nil {
			return nil, err
		}
		name := spec.SubAgentName()
		m.agents[name] = a
		m.models[name] = modelName(spec)
		m.specs[name] = spec
		m.names = append(m.names, name)
	}
	return m, nil
}

func specDescription(spec Spec) string {
	switch s := spec.(type) {
	case SubAgent:
		return s.Description
	case CompiledSubAgent:
		return s.Description
	}
	return ""
}

func (m *Middleware) Name() string { return "SubAgentMiddleware" }

// SubAgentNames lists the declared subagents in order.
func (m *Middleware) SubAgentNames() []string { return slices.Clone(m.names) }

// AddPrivateStateKeys extends the keys hidden from subagents after assembly.
func (m *Middleware) AddPrivateStateKeys(keys ...string) {
	for _, k := range keys {
		if !slices.Contains(m.opts.PrivateStateKeys, k) {
			m.opts.PrivateStateKeys = append(m.opts.PrivateStateKeys, k)
		}
	}
}

func (m *Middleware) Tools() []tool.Tool {
	if m.bg == nil {
		return []tool.Tool{m.taskTool()}
	}
	return []tool.Tool{m.taskTool(), m.taskOutputTool()}
}

func (m *Middleware) taskTool() tool.Tool {
	props := map[string]any{
		"description": map[string]any{
			"type":        "string",
			"description": "A detailed description of the task for the subagent to perform autonomously. Include all necessary context and specify the expected output format.",
		},
		"subagent_type": map[string]any{
			"type":        "string",
			"description": "The type of subagent to use. Must be one of the available agent types listed in the tool description.",
		},
	}
	if m.bg != nil {
		props["run_in_background"] = map[string]any{
			"type":        "boolean",
			"description": "Start the agent in the background and return its task id at once; its report arrives later in a <task-notification>.",
		}
	}
	return &tool.Func{
		ToolName:        TaskToolName,
		ToolDescription: m.description,
		ToolSchema: tool.Schema{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"description", "subagent_type"},
			"additionalProperties": false,
		},
		Fn:         m.task,
		ToolEffect: m.taskEffect,
	}
}

// taskEffect lets read-only and background subagents run alongside other
// reads; any other subagent may change anything.
func (m *Middleware) taskEffect(args map[string]any) tool.Effect {
	name, _ := args["subagent_type"].(string)
	bg, _ := args["run_in_background"].(bool)
	readOnly := bg && m.bg != nil
	switch s := m.specs[name].(type) {
	case SubAgent:
		readOnly = readOnly || s.ReadOnly
	case CompiledSubAgent:
		readOnly = readOnly || s.ReadOnly
	}
	if readOnly {
		return tool.Effect{Kind: tool.EffectRead}
	}
	return tool.Effect{Kind: tool.EffectExec}
}

// WrapModelCall appends the subagent system prompt, if configured.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	if m.prompt != "" {
		req.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, m.prompt)
	}
	return next(ctx, req)
}

// ValidateSpecs rejects empty or duplicate names and invalid modes.
func ValidateSpecs(specs []Spec) error {
	seen := map[string]bool{}
	for _, spec := range specs {
		name := spec.SubAgentName()
		if name == "" {
			return errors.New("subagents: every subagent needs a name")
		}
		if seen[name] {
			return fmt.Errorf("subagents: duplicate subagent name %q; each subagent must have a unique name", name)
		}
		seen[name] = true
		var mode Mode
		switch s := spec.(type) {
		case SubAgent:
			mode = s.Mode
			if s.Mode == ModeFork && len(s.Skills) > 0 {
				return fmt.Errorf("subagents: subagent %q cannot set skills under mode=fork; the parent's skills are inherited instead", name)
			}
		case CompiledSubAgent:
			mode = s.Mode
		}
		switch mode {
		case "", ModeIsolated, modeHandoff, ModeFork:
		default:
			return fmt.Errorf("subagents: subagent %q has invalid mode %q; expected %q or %q", name, mode, ModeIsolated, ModeFork)
		}
	}
	return nil
}

// compile turns a spec into a runnable agent.
func compile(spec Spec, schema runtime.Schema) (agent.Agent, error) {
	switch s := spec.(type) {
	case CompiledSubAgent:
		if s.Runnable == nil {
			return nil, fmt.Errorf("subagents: compiled subagent %q has no Runnable", s.Name)
		}
		return s.Runnable, nil
	case SubAgent:
		return compileDeclarative(s, schema)
	default:
		return nil, fmt.Errorf("subagents: %q is a %T, which the task tool cannot run", spec.SubAgentName(), spec)
	}
}

func compileDeclarative(s SubAgent, schema runtime.Schema) (agent.Agent, error) {
	if len(s.Skills) > 0 {
		return nil, notimpl.Err("subagents: SubAgent.Skills (subagent " + s.Name + ")")
	}
	if len(s.Permissions) > 0 {
		return nil, notimpl.Err("subagents: SubAgent.Permissions (subagent " + s.Name + ")")
	}
	model := s.Model
	if model == nil {
		if s.ModelSpec == "" {
			return nil, fmt.Errorf("subagents: subagent %q must specify a model", s.Name)
		}
		m, err := llm.InitChatModel(s.ModelSpec, nil)
		if err != nil {
			return nil, fmt.Errorf("subagents: subagent %q: %w", s.Name, err)
		}
		model = m
	}
	mw := slices.Clone(s.Middleware)
	if len(s.InterruptOn) > 0 {
		mw = append(mw, hitl.New(s.InterruptOn))
	}
	var sys *llm.Message
	if s.SystemPrompt != "" {
		msg := llm.NewSystemMessage(s.SystemPrompt)
		sys = &msg
	}
	a, err := agent.CreateAgent(agent.Config{
		Model:          model,
		SystemMessage:  sys,
		Tools:          s.Tools,
		Middleware:     mw,
		ResponseFormat: s.ResponseFormat,
		StateSchema:    schema,
		Name:           s.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("subagents: subagent %q: %w", s.Name, err)
	}
	return a, nil
}

func (m *Middleware) task(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	if rt != nil && rt.State != nil {
		if forked, _ := rt.State.Values[ForkedContextKey].(bool); forked {
			return textResult(forkRecursionRefusal), nil
		}
	}
	for _, key := range slices.Sorted(maps.Keys(args)) {
		if key != "description" && key != "subagent_type" && (key != "run_in_background" || m.bg == nil) {
			return nil, fmt.Errorf("unexpected argument %q; put all instructions for the subagent in `description`", key)
		}
	}
	description, _ := args["description"].(string)
	subagentType, _ := args["subagent_type"].(string)
	background, _ := args["run_in_background"].(bool)
	a, ok := m.agents[subagentType]
	if !ok {
		allowed := make([]string, len(m.names))
		for i, n := range m.names {
			allowed[i] = "`" + n + "`"
		}
		return textResult(fmt.Sprintf("We cannot invoke subagent %s because it does not exist, the only allowed types are %s",
			subagentType, strings.Join(allowed, ", "))), nil
	}
	if strings.TrimSpace(description) == "" {
		return nil, errors.New("description is required: describe the task for the subagent in full")
	}
	if rt == nil || rt.ToolCallID == "" {
		return nil, errors.New("tool call ID is required for subagent invocation")
	}
	in := m.input(m.specs[subagentType], description, rt.State)
	cfg := runtime.RunConfig{Configurable: maps.Clone(rt.Config.Configurable)}
	if cfg.Configurable == nil {
		cfg.Configurable = map[string]any{}
	}
	cfg.Configurable["ls_agent_type"] = "subagent"
	if background {
		return m.startBackground(ctx, a, in, cfg, rt, subagentType, m.models[subagentType]), nil
	}
	st, err := run(ctx, a, in, cfg, rt)
	if err != nil {
		var paused *runtime.InterruptError
		if errors.As(err, &paused) {
			return nil, fmt.Errorf("subagent %s paused for approval, which subagents do not support yet", subagentType)
		}
		return nil, fmt.Errorf("subagent %s failed: %w", subagentType, err)
	}
	return m.result(st, rt.ToolCallID, m.models[subagentType])
}

// modelName is the spec's model as "provider:model", or "" when unknown.
func modelName(spec Spec) string {
	s, ok := spec.(SubAgent)
	switch {
	case !ok:
		return ""
	case s.Model != nil:
		return specOf(s.Model)
	}
	return s.ModelSpec
}

// NamespacePrefix starts the namespace of events forwarded from a subagent;
// the task tool call ID follows it.
const NamespacePrefix = "task:"

// run invokes the subagent. When the parent is streaming, the subagent's
// state snapshots are forwarded under "task:<tool call id>" so a UI can show
// its progress, and the last snapshot is the final state.
func run(ctx context.Context, a agent.Agent, in agent.Input, cfg runtime.RunConfig, rt *tool.Runtime) (*runtime.State, error) {
	if rt.Emit == nil {
		return a.Invoke(ctx, in, cfg)
	}
	ns := NamespacePrefix + rt.ToolCallID
	var final *runtime.State
	for ev, err := range a.Stream(ctx, in, cfg, runtime.StreamValues) {
		if err != nil {
			return nil, err
		}
		if st, ok := ev.Data.(*runtime.State); ok && ev.Mode == runtime.StreamValues && len(ev.Namespace) == 0 {
			final = st
		}
		ev.Namespace = append([]string{ns}, ev.Namespace...)
		rt.Emit(ev)
	}
	return final, nil
}

// filterValues drops excluded and private keys.
func (m *Middleware) filterValues(values map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range values {
		if slices.Contains(ExcludedStateKeys, k) || slices.Contains(m.opts.PrivateStateKeys, k) {
			continue
		}
		out[k] = v
	}
	return out
}

// result turns the subagent's final state into a Command carrying its
// shareable state and one tool message with its report and token usage.
func (m *Middleware) result(st *runtime.State, toolCallID, model string) (*tool.Result, error) {
	content, err := reportText(st)
	if err != nil {
		return nil, err
	}
	msg := llm.NewToolMessage(toolCallID, TaskToolName, content)
	msg.Metadata = withUsage(msg.Metadata, usageOf(st, model))
	upd := &runtime.Update{Messages: []llm.Message{msg}}
	if values := m.filterValues(st.Values); len(values) > 0 {
		upd.Values = values
	}
	return &tool.Result{Command: &runtime.Command{Update: upd}}, nil
}

func textResult(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}
}
