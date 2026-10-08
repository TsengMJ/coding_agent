// Package profiles tunes model construction (ProviderProfile) and harness
// behavior (HarnessProfile) per provider or "provider:model" key.
package profiles

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// GeneralPurposeSubagentProfile adjusts the auto-added general-purpose subagent.
type GeneralPurposeSubagentProfile struct {
	// Enabled nil keeps the default (enabled); false removes the subagent.
	Enabled      *bool
	Description  *string
	SystemPrompt *string
}

// HarnessProfile shapes how the agent runs for a matching model.
type HarnessProfile struct {
	// BaseSystemPrompt replaces the BASE slot (USER -> BASE -> SUFFIX).
	BaseSystemPrompt   *string
	SystemPromptSuffix *string
	// ToolDescriptionOverrides rewrites tool descriptions by tool name.
	ToolDescriptionOverrides map[string]string
	// ExcludedTools are hidden from the model by the tool-exclusion middleware.
	ExcludedTools []string
	// ExcludedMiddleware removes middleware by Name(); scaffolding cannot be excluded.
	ExcludedMiddleware []string
	// ExtraMiddleware builds middleware appended to the tail of each stack.
	ExtraMiddleware        func() []agent.Middleware
	GeneralPurposeSubagent *GeneralPurposeSubagentProfile
}

// HarnessProfileConfig is the declarative (YAML/JSON) subset of HarnessProfile.
type HarnessProfileConfig struct {
	BaseSystemPrompt         *string                        `json:"base_system_prompt,omitempty"`
	SystemPromptSuffix       *string                        `json:"system_prompt_suffix,omitempty"`
	ToolDescriptionOverrides map[string]string              `json:"tool_description_overrides,omitempty"`
	ExcludedTools            []string                       `json:"excluded_tools,omitempty"`
	ExcludedMiddleware       []string                       `json:"excluded_middleware,omitempty"`
	GeneralPurposeSubagent   *GeneralPurposeSubagentProfile `json:"general_purpose_subagent,omitempty"`
}

// ToProfile converts the declarative config to a runtime profile.
func (c HarnessProfileConfig) ToProfile() HarnessProfile {
	return HarnessProfile{
		BaseSystemPrompt:         c.BaseSystemPrompt,
		SystemPromptSuffix:       c.SystemPromptSuffix,
		ToolDescriptionOverrides: c.ToolDescriptionOverrides,
		ExcludedTools:            c.ExcludedTools,
		ExcludedMiddleware:       c.ExcludedMiddleware,
		GeneralPurposeSubagent:   c.GeneralPurposeSubagent,
	}
}

var (
	harnessMu       sync.RWMutex
	harnessProfiles = map[string]HarnessProfile{}
)

// RegisterHarnessProfile registers p under key, merging additively with any
// existing profile for the same key.
func RegisterHarnessProfile(key string, p HarnessProfile) error {
	if err := validateKey(key); err != nil {
		return err
	}
	harnessMu.Lock()
	defer harnessMu.Unlock()
	if existing, ok := harnessProfiles[key]; ok {
		p = MergeHarnessProfiles(existing, p)
	}
	harnessProfiles[key] = p
	return nil
}

func validateKey(key string) error {
	provider, model, found := strings.Cut(key, ":")
	if key == "" || found && (provider == "" || model == "") {
		return fmt.Errorf("profiles: invalid key %q; want \"provider\" or \"provider:model\"", key)
	}
	return nil
}

// HarnessProfileFor resolves the merged profile for a model: provider-level
// first, then "provider:model" layered on top. Unmatched models get an empty
// profile. spec, when set, is used instead of the model's reported identity.
func HarnessProfileFor(model llm.ChatModel, spec string) (HarnessProfile, error) {
	if spec != "" {
		if err := validateKey(spec); err != nil {
			return HarnessProfile{}, err
		}
		p, _ := lookup(spec)
		return p, nil
	}
	if model == nil {
		return HarnessProfile{}, nil
	}
	info := model.Info()
	var candidates []string
	if info.Provider != "" && info.Model != "" {
		candidates = append(candidates, info.Provider+":"+info.Model)
	}
	if strings.Contains(info.Model, ":") {
		candidates = append(candidates, info.Model)
	}
	for _, c := range candidates {
		if validateKey(c) != nil {
			continue
		}
		if p, exact := lookup(c); exact {
			return p, nil
		}
	}
	provider := info.Provider
	if provider == "" {
		provider, _, _ = strings.Cut(info.Model, ":")
	}
	if provider == "" {
		return HarnessProfile{}, nil
	}
	harnessMu.RLock()
	defer harnessMu.RUnlock()
	return harnessProfiles[provider], nil
}

// lookup merges the provider profile with the exact one for spec; exact
// reports whether spec itself was registered.
func lookup(spec string) (p HarnessProfile, exact bool) {
	harnessMu.RLock()
	defer harnessMu.RUnlock()
	provider, _, hasModel := strings.Cut(spec, ":")
	exactP, exact := harnessProfiles[spec]
	if !hasModel {
		return exactP, exact
	}
	base, hasBase := harnessProfiles[provider]
	switch {
	case exact && hasBase:
		return MergeHarnessProfiles(base, exactP), true
	case exact:
		return exactP, true
	default:
		return base, false
	}
}

// MergeHarnessProfiles layers override on base (`_merge_profiles`): set
// scalars win, maps merge per key, exclusion lists union, and extra
// middleware replaces same-named entries in place.
func MergeHarnessProfiles(base, override HarnessProfile) HarnessProfile {
	out := HarnessProfile{
		BaseSystemPrompt:       firstSet(override.BaseSystemPrompt, base.BaseSystemPrompt),
		SystemPromptSuffix:     firstSet(override.SystemPromptSuffix, base.SystemPromptSuffix),
		ExcludedTools:          union(base.ExcludedTools, override.ExcludedTools),
		ExcludedMiddleware:     union(base.ExcludedMiddleware, override.ExcludedMiddleware),
		ExtraMiddleware:        mergeExtra(base.ExtraMiddleware, override.ExtraMiddleware),
		GeneralPurposeSubagent: mergeGeneralPurpose(base.GeneralPurposeSubagent, override.GeneralPurposeSubagent),
	}
	if len(base.ToolDescriptionOverrides)+len(override.ToolDescriptionOverrides) > 0 {
		out.ToolDescriptionOverrides = maps.Clone(base.ToolDescriptionOverrides)
		if out.ToolDescriptionOverrides == nil {
			out.ToolDescriptionOverrides = map[string]string{}
		}
		maps.Copy(out.ToolDescriptionOverrides, override.ToolDescriptionOverrides)
	}
	return out
}

func firstSet[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

func union(a, b []string) []string {
	out := slices.Clone(a)
	for _, s := range b {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func mergeExtra(base, override func() []agent.Middleware) func() []agent.Middleware {
	if base == nil || override == nil {
		return firstNonNil(override, base)
	}
	return func() []agent.Middleware {
		over := override()
		byName := map[string]agent.Middleware{}
		for _, m := range over {
			byName[m.Name()] = m
		}
		var merged []agent.Middleware
		replaced := map[string]bool{}
		for _, m := range base() {
			o, ok := byName[m.Name()]
			if !ok {
				merged = append(merged, m)
				continue
			}
			if !replaced[m.Name()] {
				merged = append(merged, o)
				replaced[m.Name()] = true
			}
		}
		for _, m := range over {
			if !replaced[m.Name()] {
				merged = append(merged, m)
			}
		}
		return merged
	}
}

func firstNonNil(a, b func() []agent.Middleware) func() []agent.Middleware {
	if a != nil {
		return a
	}
	return b
}

func mergeGeneralPurpose(base, override *GeneralPurposeSubagentProfile) *GeneralPurposeSubagentProfile {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	return &GeneralPurposeSubagentProfile{
		Enabled:      firstSet(override.Enabled, base.Enabled),
		Description:  firstSet(override.Description, base.Description),
		SystemPrompt: firstSet(override.SystemPrompt, base.SystemPrompt),
	}
}

// ApplyPrompt assembles basePrompt with the profile's BASE and SUFFIX slots:
// BaseSystemPrompt replaces basePrompt, and SystemPromptSuffix is appended
// after a blank line.
func ApplyPrompt(p HarnessProfile, basePrompt string) string {
	prompt := basePrompt
	if p.BaseSystemPrompt != nil {
		prompt = *p.BaseSystemPrompt
	}
	if p.SystemPromptSuffix != nil {
		if prompt == "" {
			return *p.SystemPromptSuffix
		}
		prompt += "\n\n" + *p.SystemPromptSuffix
	}
	return prompt
}

// Materialize builds the profile's extra middleware, or nil.
func (p HarnessProfile) Materialize() []agent.Middleware {
	if p.ExtraMiddleware == nil {
		return nil
	}
	return p.ExtraMiddleware()
}

// resetHarnessProfiles clears the registry (tests only).
func resetHarnessProfiles() {
	harnessMu.Lock()
	defer harnessMu.Unlock()
	clear(harnessProfiles)
}
