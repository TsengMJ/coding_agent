// Package deepagents assembles a "deep agent": a model/tool loop preloaded
// with filesystem, subagent, summarization and other harness middleware
// (Python `deepagents.graph.create_deep_agent`).
package deepagents

import (
	"errors"
	"fmt"
	"slices"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/filesystem"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/memory"
	"github.com/tsengmingchun/deepagents_golang/middleware/patchtoolcalls"
	"github.com/tsengmingchun/deepagents_golang/middleware/permissions"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
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

// CreateDeepAgent builds a deep agent.
//
// Main stack order: Skills (if skills) -> Filesystem -> SubAgent (if any sync
// subagents) -> Summarization -> PatchToolCalls -> AsyncSubAgent (if any) ->
// caller middleware -> profile extra middleware -> prompt caching ->
// Memory (if memory) -> HITL (if interrupts) -> UnsupportedContent ->
// ToolExclusion (if profile excludes tools).
//
// The default stack is only partly assembled: Filesystem runs when a backend
// is given, followed by the caller's middleware. Options that depend on the
// rest of the stack return notimpl.ErrNotImplemented.
func CreateDeepAgent(opts ...Option) (agent.Agent, error) {
	var o Options
	for _, opt := range opts {
		opt(&o)
	}
	if err := rejectUnsupported(o); err != nil {
		return nil, err
	}
	model, err := resolveModel(o)
	if err != nil {
		return nil, err
	}
	stack, err := buildMiddleware(o, model)
	if err != nil {
		return nil, err
	}
	return agent.CreateAgent(agent.Config{
		Model:          model,
		SystemMessage:  resolveSystemMessage(o),
		Tools:          o.Tools,
		Middleware:     stack,
		StateSchema:    o.StateSchema,
		Checkpointer:   o.Checkpointer,
		Store:          o.Store,
		Name:           o.Name,
		Debug:          o.Debug,
		RecursionLimit: runtime.DefaultRecursionLimit,
	})
}

func rejectUnsupported(o Options) error {
	unsupported := []struct {
		option string
		set    bool
	}{
		{"WithSubAgents", len(o.SubAgents) > 0},
		{"WithSkills", len(o.Skills) > 0},
		{"WithPermissions", len(o.Permissions) > 0},
		{"WithResponseFormat", o.ResponseFormat != nil},
	}
	for _, u := range unsupported {
		if u.set {
			return notimpl.Err("deepagents.CreateDeepAgent(" + u.option + ")")
		}
	}
	return nil
}

// buildMiddleware places FilesystemMiddleware and SummarizationMiddleware
// (when a backend is set) and PatchToolCallsMiddleware ahead of the caller's
// middleware, and HumanInTheLoopMiddleware (when InterruptOn is set) last,
// so its AfterModel runs first and interrupts before other hooks change state.
func buildMiddleware(o Options, model llm.ChatModel) ([]agent.Middleware, error) {
	var stack []agent.Middleware
	if o.Backend != nil {
		fs, err := filesystem.New(filesystem.Options{Backend: o.Backend})
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		summ, err := summarization.NewForModel(model, o.Backend)
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		stack = append(stack, fs, summ)
	}
	stack = append(stack, patchtoolcalls.New())
	stack, err := mergeMiddleware(stack, o.Middleware)
	if err != nil {
		return nil, err
	}
	if len(o.Memory) > 0 {
		if o.Backend == nil {
			return nil, errors.New("deepagents.CreateDeepAgent: WithMemory requires WithBackend")
		}
		mem, err := memory.New(memory.Options{Backend: o.Backend, Sources: o.Memory})
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		stack = append(stack, mem)
	}
	if len(o.InterruptOn) > 0 {
		stack = append(stack, hitl.New(o.InterruptOn))
	}
	return stack, nil
}

// mergeMiddleware appends extra to base; an extra entry whose Name matches a
// base entry replaces it in place instead. Names within extra must be unique.
func mergeMiddleware(base, extra []agent.Middleware) ([]agent.Middleware, error) {
	out := slices.Clone(base)
	seen := map[string]bool{}
	for _, m := range extra {
		name := m.Name()
		if seen[name] {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: duplicate middleware %q", name)
		}
		seen[name] = true
		i := slices.IndexFunc(out[:len(base)], func(b agent.Middleware) bool { return b.Name() == name })
		if i >= 0 {
			out[i] = m
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func resolveModel(o Options) (llm.ChatModel, error) {
	if o.Model != nil {
		return o.Model, nil
	}
	if o.ModelSpec == "" {
		return nil, errors.New("deepagents.CreateDeepAgent: WithModel or WithModelSpec is required")
	}
	return llm.InitChatModel(o.ModelSpec, nil)
}

func resolveSystemMessage(o Options) *llm.Message {
	if o.SystemMessage != nil {
		return o.SystemMessage
	}
	if o.SystemPrompt == "" {
		return nil
	}
	msg := llm.NewSystemMessage(o.SystemPrompt)
	return &msg
}
