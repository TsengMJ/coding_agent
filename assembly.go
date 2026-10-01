package deepagents

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/filesystem"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/memory"
	"github.com/tsengmingchun/deepagents_golang/middleware/patchtoolcalls"
	"github.com/tsengmingchun/deepagents_golang/middleware/permissions"
	"github.com/tsengmingchun/deepagents_golang/middleware/skills"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
	"github.com/tsengmingchun/deepagents_golang/middleware/toolexclusion"
	"github.com/tsengmingchun/deepagents_golang/profiles"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// assembly carries what every stack of one CreateDeepAgent call shares.
type assembly struct {
	o       Options
	model   llm.ChatModel
	profile profiles.HarnessProfile
	tools   []tool.Tool
	// matched records excluded-middleware names that removed something in
	// any stack; an exclusion that matches nothing is a configuration error.
	matched map[string]bool
}

func newAssembly(o Options, model llm.ChatModel) (*assembly, error) {
	spec := ""
	if o.Model == nil {
		spec = o.ModelSpec
	}
	profile, err := profiles.HarnessProfileFor(model, spec)
	if err != nil {
		return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
	}
	if err := validateExcluded(profile); err != nil {
		return nil, err
	}
	return &assembly{
		o:       o,
		model:   model,
		profile: profile,
		tools:   tool.ApplyDescriptionOverrides(o.Tools, profile.ToolDescriptionOverrides),
		matched: map[string]bool{},
	}, nil
}

// validateExcluded rejects exclusions of required scaffolding and of
// private (underscore) names.
func validateExcluded(p profiles.HarnessProfile) error {
	for _, name := range p.ExcludedMiddleware {
		switch {
		case slices.Contains(RequiredMiddleware, name):
			return fmt.Errorf("deepagents.CreateDeepAgent: harness profile cannot exclude %q; core features depend on it", name)
		case strings.HasPrefix(name, "_"):
			return fmt.Errorf("deepagents.CreateDeepAgent: harness profile cannot exclude private middleware %q", name)
		}
	}
	return nil
}

// buildMiddleware assembles the main stack:
// Skills -> Filesystem -> SubAgent -> Summarization -> PatchToolCalls -> [caller] ->
// profile extra -> Memory -> HITL -> ToolExclusion.
// Filesystem and Summarization need a backend; SubAgent is present when any
// subagent is configured (including the default general-purpose one).
func (a *assembly) buildMiddleware() ([]agent.Middleware, error) {
	o := a.o
	specs, err := a.subagentSpecs()
	if err != nil {
		return nil, err
	}
	var stack []agent.Middleware
	var sub *subagents.Middleware
	if len(o.Skills) > 0 {
		sk, err := a.skills(o.Skills)
		if err != nil {
			return nil, err
		}
		stack = append(stack, sk)
	}
	if o.Backend != nil {
		fs, err := a.filesystem(o.Backend)
		if err != nil {
			return nil, err
		}
		stack = append(stack, fs)
	}
	if len(specs) > 0 {
		sub, err = subagents.New(subagents.Options{
			Backend:         o.Backend,
			SubAgents:       specs,
			TaskDescription: a.profile.ToolDescriptionOverrides[subagents.TaskToolName],
			StateSchema:     o.StateSchema,
			Background:      o.BackgroundTasks,
		})
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		stack = append(stack, sub)
	}
	if o.Backend != nil {
		summ, err := summarization.NewForModel(a.model, o.Backend)
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		stack = append(stack, summ)
	}
	stack = append(stack, patchtoolcalls.New())
	coreLen := len(stack)

	stack = append(stack, a.profile.Materialize()...)
	if len(o.Memory) > 0 {
		if o.Backend == nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: WithMemory requires WithBackend")
		}
		mem, err := memory.New(memory.Options{Backend: o.Backend, Sources: o.Memory})
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		stack = append(stack, mem)
	}
	generated, err := permissions.BuildInterruptOn(o.Permissions)
	if err != nil {
		return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
	}
	if interruptOn := hitl.Merge(generated, o.InterruptOn); len(interruptOn) > 0 {
		stack = append(stack, hitl.New(interruptOn))
	}

	stack, coreLen = a.exclude(stack, coreLen)
	stack, err = mergeMiddleware(stack, o.Middleware, coreLen)
	if err != nil {
		return nil, err
	}
	stack, _ = a.exclude(stack, len(stack))
	if len(a.profile.ExcludedTools) > 0 {
		stack = append(stack, toolexclusion.New(a.profile.ExcludedTools))
	}
	if err := a.verifyExcludedCoverage(); err != nil {
		return nil, err
	}
	stack = placeSummarizationLast(stack)
	shareSchemaPrivacy(stack, o.StateSchema)
	return stack, nil
}

func (a *assembly) skills(sources []string) (*skills.Middleware, error) {
	sk, err := skills.New(skills.Options{Backend: a.o.Backend, Sources: skills.SourcesFromPaths(sources)})
	if err != nil {
		return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
	}
	return sk, nil
}

func (a *assembly) filesystem(b backend.Backend) (*filesystem.Middleware, error) {
	fs, err := filesystem.New(filesystem.Options{
		Backend:                b,
		CustomToolDescriptions: a.profile.ToolDescriptionOverrides,
		Permissions:            a.o.Permissions,
	})
	if err != nil {
		return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
	}
	return fs, nil
}

// subagentSpecs resolves the caller's subagents and prepends the default
// general-purpose subagent unless the profile disables it or the caller
// supplies one with the same name.
func (a *assembly) subagentSpecs() ([]subagents.Spec, error) {
	var specs []subagents.Spec
	hasGP := false
	for _, spec := range a.o.SubAgents {
		hasGP = hasGP || spec.SubAgentName() == subagents.GeneralPurposeName
		switch s := spec.(type) {
		case subagents.CompiledSubAgent:
			specs = append(specs, s)
		case subagents.SubAgent:
			resolved, err := a.resolveSubAgent(s)
			if err != nil {
				return nil, err
			}
			specs = append(specs, resolved)
		default:
			return nil, notimpl.Err(fmt.Sprintf("deepagents.CreateDeepAgent(WithSubAgents: %T %q)", spec, spec.SubAgentName()))
		}
	}
	gp := a.profile.GeneralPurposeSubagent
	if hasGP || gp != nil && gp.Enabled != nil && !*gp.Enabled {
		return specs, nil
	}
	spec, err := a.generalPurpose()
	if err != nil {
		return nil, err
	}
	return append([]subagents.Spec{spec}, specs...), nil
}

// generalPurpose builds the default subagent: the parent's model and tools
// with its own Filesystem / Summarization / PatchToolCalls stack. Caller
// middleware that replaces one of those slots by name is inherited.
//
// Parent InterruptOn is not inherited: a subagent cannot pause the parent
// run for approval yet, so review would fail every gated call. Parent
// permissions are inherited with interrupt rules hardened to deny.
func (a *assembly) generalPurpose() (subagents.SubAgent, error) {
	mw, err := a.subagentBase(a.model, a.profile, a.o.Permissions, a.o.Skills)
	if err != nil {
		return subagents.SubAgent{}, err
	}
	slots := map[string]bool{}
	for _, m := range mw {
		slots[m.Name()] = true
	}
	mw = append(mw, a.profile.Materialize()...)
	mw, _ = a.exclude(mw, len(mw))
	var inherited []agent.Middleware
	for _, m := range a.o.Middleware {
		if slots[m.Name()] {
			inherited = append(inherited, m)
		}
	}
	if mw, err = mergeMiddleware(mw, inherited, len(mw)); err != nil {
		return subagents.SubAgent{}, err
	}
	mw, _ = a.exclude(mw, len(mw))
	if len(a.profile.ExcludedTools) > 0 {
		mw = append(mw, toolexclusion.New(a.profile.ExcludedTools))
	}
	mw = placeSummarizationLast(mw)

	spec := subagents.SubAgent{
		Name:         subagents.GeneralPurposeName,
		Description:  subagents.GeneralPurposeDescription,
		SystemPrompt: profiles.ApplyPrompt(a.profile, subagents.DefaultSubagentPrompt),
		Model:        a.model,
		Tools:        append([]tool.Tool{}, a.tools...),
		Middleware:   mw,
	}
	if gp := a.profile.GeneralPurposeSubagent; gp != nil {
		if gp.Description != nil {
			spec.Description = *gp.Description
		}
		if gp.SystemPrompt != nil {
			spec.SystemPrompt = *gp.SystemPrompt
			if a.profile.SystemPromptSuffix != nil {
				spec.SystemPrompt += "\n\n" + *a.profile.SystemPromptSuffix
			}
		}
	}
	return spec, nil
}

// resolveSubAgent fills in a declarative subagent: model (default: the
// parent's), tools (nil inherits the parent's), the base middleware stack
// for its model's profile, and the profile prompt overlay. Permissions
// default to the parent's; interrupt rules become deny (see generalPurpose).
func (a *assembly) resolveSubAgent(s subagents.SubAgent) (subagents.SubAgent, error) {
	var skillSources []string
	if s.Mode != subagents.ModeFork {
		skillSources, s.Skills = s.Skills, nil
	}
	perms := s.Permissions
	if perms == nil {
		perms = a.o.Permissions
	}
	model, spec := s.Model, ""
	if model == nil && s.ModelSpec != "" {
		m, err := llm.InitChatModel(s.ModelSpec, nil)
		if err != nil {
			return s, fmt.Errorf("deepagents.CreateDeepAgent: subagent %q: %w", s.Name, err)
		}
		model, spec = m, s.ModelSpec
	}
	if model == nil {
		model = a.model
	}
	profile, err := profiles.HarnessProfileFor(model, spec)
	if err != nil {
		return s, fmt.Errorf("deepagents.CreateDeepAgent: subagent %q: %w", s.Name, err)
	}
	if err := validateExcluded(profile); err != nil {
		return s, err
	}
	mw, err := a.subagentBase(model, profile, perms, skillSources)
	if err != nil {
		return s, fmt.Errorf("%w (subagent %q)", err, s.Name)
	}
	coreLen := len(mw)
	mw = append(mw, profile.Materialize()...)
	sub := &assembly{o: a.o, model: model, profile: profile, matched: map[string]bool{}}
	mw, coreLen = sub.exclude(mw, coreLen)
	if mw, err = mergeMiddleware(mw, s.Middleware, coreLen); err != nil {
		return s, err
	}
	mw, _ = sub.exclude(mw, len(mw))
	if err := sub.verifyExcludedCoverage(); err != nil {
		return s, err
	}
	if len(profile.ExcludedTools) > 0 {
		mw = append(mw, toolexclusion.New(profile.ExcludedTools))
	}
	mw = placeSummarizationLast(mw)

	tools := s.Tools
	if tools == nil {
		tools = a.o.Tools
	}
	s.Model, s.ModelSpec = model, ""
	s.Permissions = nil
	s.Tools = append([]tool.Tool{}, tool.ApplyDescriptionOverrides(tools, profile.ToolDescriptionOverrides)...)
	s.Middleware = mw
	s.SystemPrompt = profiles.ApplyPrompt(profile, s.SystemPrompt)
	return s, nil
}

// subagentBase is the core stack every declarative subagent starts from:
// Filesystem -> Summarization -> PatchToolCalls -> Skills (if any).
func (a *assembly) subagentBase(model llm.ChatModel, profile profiles.HarnessProfile, perms []permissions.FilesystemPermission, skillSources []string) ([]agent.Middleware, error) {
	var mw []agent.Middleware
	if b := a.o.Backend; b != nil {
		fs, err := filesystem.New(filesystem.Options{
			Backend:                b,
			CustomToolDescriptions: profile.ToolDescriptionOverrides,
			Permissions:            permissions.InterruptAsDeny(perms),
		})
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		summ, err := summarization.NewForModel(model, b)
		if err != nil {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: %w", err)
		}
		mw = append(mw, fs, summ)
	}
	mw = append(mw, patchtoolcalls.New())
	if len(skillSources) > 0 {
		sk, err := a.skills(skillSources)
		if err != nil {
			return nil, err
		}
		mw = append(mw, sk)
	}
	return mw, nil
}

// exclude drops middleware named in the profile's ExcludedMiddleware and
// returns the stack with coreLen adjusted for removed core entries.
func (a *assembly) exclude(stack []agent.Middleware, coreLen int) ([]agent.Middleware, int) {
	if len(a.profile.ExcludedMiddleware) == 0 {
		return stack, coreLen
	}
	var out []agent.Middleware
	newCore := coreLen
	for i, m := range stack {
		if slices.Contains(a.profile.ExcludedMiddleware, m.Name()) {
			a.matched[m.Name()] = true
			if i < coreLen {
				newCore--
			}
			continue
		}
		out = append(out, m)
	}
	return out, newCore
}

func (a *assembly) verifyExcludedCoverage() error {
	for _, name := range a.profile.ExcludedMiddleware {
		if !a.matched[name] {
			return fmt.Errorf("deepagents.CreateDeepAgent: harness profile excludes middleware %q, which is not in any stack", name)
		}
	}
	return nil
}

// mergeMiddleware applies caller middleware: an entry whose Name matches one
// in stack replaces it in place; others are inserted at coreLen, after the
// core stack and before the tail. Names within extra must be unique.
func mergeMiddleware(stack, extra []agent.Middleware, coreLen int) ([]agent.Middleware, error) {
	out := slices.Clone(stack)
	var added []agent.Middleware
	seen := map[string]bool{}
	for _, m := range extra {
		name := m.Name()
		if seen[name] {
			return nil, fmt.Errorf("deepagents.CreateDeepAgent: duplicate middleware %q", name)
		}
		seen[name] = true
		if i := slices.IndexFunc(out, func(b agent.Middleware) bool { return b.Name() == name }); i >= 0 {
			out[i] = m
			continue
		}
		added = append(added, m)
	}
	return slices.Insert(out, coreLen, added...), nil
}

// placeSummarizationLast lets it measure the fully assembled model request,
// including memory, skills, caller prompts and filtered tools.
func placeSummarizationLast(stack []agent.Middleware) []agent.Middleware {
	for i, mw := range stack {
		if mw.Name() == "SummarizationMiddleware" {
			out := slices.Delete(slices.Clone(stack), i, i+1)
			return append(out, mw)
		}
	}
	return stack
}

// shareSchemaPrivacy tells the subagent middleware which state keys are
// private anywhere in the stack, so they are not passed to subagents.
func shareSchemaPrivacy(stack []agent.Middleware, base runtime.Schema) {
	schemas := []runtime.Schema{base}
	for _, m := range stack {
		if p, ok := m.(agent.StateSchemaProvider); ok {
			schemas = append(schemas, p.StateSchema())
		}
	}
	keys := runtime.PrivateKeys(schemas...)
	for _, m := range stack {
		if sub, ok := m.(*subagents.Middleware); ok {
			sub.AddPrivateStateKeys(keys...)
		}
	}
}
