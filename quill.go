// Package quill assembles a Quill agent: a model/tool loop preloaded
// with filesystem, subagent, summarization and other harness middleware.
package quill

import (
	"errors"
	"fmt"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/langsmith"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/filesystem"
	"github.com/tsengmingchun/quill/middleware/hitl"
	"github.com/tsengmingchun/quill/middleware/permissions"
	"github.com/tsengmingchun/quill/middleware/subagents"
	"github.com/tsengmingchun/quill/profiles"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
	"github.com/tsengmingchun/quill/tracing"
)

// Version is the Go port version.
const Version = "0.0.1"

// RequiredMiddleware names scaffolding that profiles may not exclude.
var RequiredMiddleware = []string{
	(&filesystem.Middleware{}).Name(),
	(&subagents.Middleware{}).Name(),
}

// Options holds every create_deep_agent argument.
type Options struct {
	// Model is used directly; ModelSpec ("provider:model") is resolved when Model is nil.
	Model     llm.ChatModel
	ModelSpec string
	Tools     []tool.Tool
	// SystemPrompt is the USER slot; SystemMessage takes precedence and keeps cache markers.
	SystemPrompt  string
	SystemMessage *llm.Message
	// Middleware is inserted between the base stack and the tail stack;
	// entries whose Name matches a base entry replace it in place.
	Middleware []agent.Middleware
	SubAgents  []subagents.Spec
	Skills     []string
	Memory     []string
	// Permissions apply to the main agent and are inherited by subagents.
	Permissions    []permissions.FilesystemPermission
	Backend        backend.Backend
	InterruptOn    map[string]hitl.InterruptOnConfig
	ResponseFormat agent.ResponseFormat
	StateSchema    runtime.Schema
	Checkpointer   runtime.Checkpointer
	Store          runtime.Store
	Name           string
	Debug          bool
	// BackgroundTasks lets `task` run subagents in the background.
	BackgroundTasks bool
	// Tracer records run trees; subagents join through ctx. Unless
	// TracerSet, the environment's LangSmith client is used (langsmith.Default),
	// which is nil — no tracing — unless LANGSMITH_TRACING=true.
	Tracer    tracing.Tracer
	TracerSet bool
}

// Option mutates Options.
type Option func(*Options)

func WithModel(m llm.ChatModel) Option       { return func(o *Options) { o.Model = m } }
func WithModelSpec(spec string) Option       { return func(o *Options) { o.ModelSpec = spec } }
func WithTools(t ...tool.Tool) Option        { return func(o *Options) { o.Tools = append(o.Tools, t...) } }
func WithSystemPrompt(p string) Option       { return func(o *Options) { o.SystemPrompt = p } }
func WithSystemMessage(m llm.Message) Option { return func(o *Options) { o.SystemMessage = &m } }
func WithSkills(paths ...string) Option {
	return func(o *Options) { o.Skills = append(o.Skills, paths...) }
}
func WithMemory(paths ...string) Option {
	return func(o *Options) { o.Memory = append(o.Memory, paths...) }
}
func WithBackend(b backend.Backend) Option    { return func(o *Options) { o.Backend = b } }
func WithStateSchema(s runtime.Schema) Option { return func(o *Options) { o.StateSchema = s } }
func WithCheckpointer(c runtime.Checkpointer) Option {
	return func(o *Options) { o.Checkpointer = c }
}
func WithStore(s runtime.Store) Option { return func(o *Options) { o.Store = s } }
func WithName(n string) Option         { return func(o *Options) { o.Name = n } }
func WithDebug(d bool) Option          { return func(o *Options) { o.Debug = d } }

// WithBackgroundTasks adds run_in_background to `task` and the task_output tool.
func WithBackgroundTasks() Option { return func(o *Options) { o.BackgroundTasks = true } }

// WithTracer records runs with t instead of the environment's LangSmith
// client; nil turns tracing off.
func WithTracer(t tracing.Tracer) Option {
	return func(o *Options) { o.Tracer, o.TracerSet = t, true }
}

func WithMiddleware(m ...agent.Middleware) Option {
	return func(o *Options) { o.Middleware = append(o.Middleware, m...) }
}

func WithSubAgents(s ...subagents.Spec) Option {
	return func(o *Options) { o.SubAgents = append(o.SubAgents, s...) }
}

func WithPermissions(p ...permissions.FilesystemPermission) Option {
	return func(o *Options) { o.Permissions = append(o.Permissions, p...) }
}

func WithInterruptOn(cfg map[string]hitl.InterruptOnConfig) Option {
	return func(o *Options) { o.InterruptOn = cfg }
}

func WithResponseFormat(f agent.ResponseFormat) Option {
	return func(o *Options) { o.ResponseFormat = f }
}

// CreateAgent builds a Quill agent.
//
// Without WithBackend, files live in agent state (backend.StateBackend).
//
// Main stack order: Skills (if skills) -> Filesystem -> SubAgent -> Summarization
// -> PatchToolCalls -> caller middleware -> profile extra
// middleware -> Memory (if memory) -> HITL (if interrupts) -> ToolExclusion
// (if the profile excludes tools). Caller middleware whose Name matches an
// entry replaces it in place. A general-purpose subagent is added unless the
// harness profile disables it or a subagent of that name is given.
//
// Permissions deny or filter filesystem tool paths; interrupt rules add HITL
// configs merged under WithInterruptOn (user entries win per tool).
//
// Skills are listed in the prompt of the main agent and the general-purpose
// subagent; declarative subagents use their own SubAgent.Skills.
//
// Response formats, async subagents, prompt caching
// and unsupported-content handling are not implemented yet.
func CreateAgent(opts ...Option) (agent.Agent, error) {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	if o.Backend == nil {
		o.Backend = backend.NewStateBackend()
	}
	model, err := resolveModel(o)
	if err != nil {
		return nil, err
	}
	a, err := newAssembly(o, model)
	if err != nil {
		return nil, err
	}
	stack, err := a.buildMiddleware()
	if err != nil {
		return nil, err
	}
	tracer, err := resolveTracer(o)
	if err != nil {
		return nil, err
	}
	return agent.CreateAgent(agent.Config{
		Tracer:         tracer,
		Model:          model,
		SystemMessage:  resolveSystemMessage(o, profiles.ApplyPrompt(a.profile, "")),
		Tools:          a.tools,
		Middleware:     stack,
		StateSchema:    o.StateSchema,
		Checkpointer:   o.Checkpointer,
		Store:          o.Store,
		Name:           o.Name,
		Debug:          o.Debug,
		RecursionLimit: runtime.DefaultRecursionLimit,
		ResponseFormat: o.ResponseFormat,
	})
}

func resolveTracer(o Options) (tracing.Tracer, error) {
	if o.TracerSet {
		return o.Tracer, nil
	}
	c, err := langsmith.Default()
	if err != nil {
		return nil, fmt.Errorf("quill.CreateAgent: %w", err)
	}
	if c == nil {
		return nil, nil
	}
	return c, nil
}

func resolveModel(o Options) (llm.ChatModel, error) {
	if o.Model != nil {
		return o.Model, nil
	}
	if o.ModelSpec == "" {
		return nil, errors.New("quill.CreateAgent: WithModel or WithModelSpec is required")
	}
	return llm.InitChatModel(o.ModelSpec, nil)
}

// resolveSystemMessage puts the caller's prompt first and the profile's
// base prompt after it.
func resolveSystemMessage(o Options, profilePrompt string) *llm.Message {
	if o.SystemMessage != nil {
		if profilePrompt == "" {
			return o.SystemMessage
		}
		return agent.AppendToSystemMessage(o.SystemMessage, profilePrompt)
	}
	prompt := o.SystemPrompt
	if profilePrompt != "" {
		if prompt != "" {
			prompt += "\n\n"
		}
		prompt += profilePrompt
	}
	if prompt == "" {
		return nil
	}
	msg := llm.NewSystemMessage(prompt)
	return &msg
}
