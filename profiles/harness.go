// Package profiles tunes model construction (ProviderProfile) and harness
// behavior (HarnessProfile) per provider or "provider:model" key
// (Python `deepagents.profiles`).
package profiles

import (
	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
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

// RegisterHarnessProfile registers p under key, merging additively with any
// existing profile for the same key.
func RegisterHarnessProfile(key string, p HarnessProfile) error {
	return notimpl.Err("profiles.RegisterHarnessProfile")
}

// HarnessProfileFor resolves the merged profile for a model: provider-level
// first, then "provider:model" layered on top. Unmatched models get an empty profile.
func HarnessProfileFor(model llm.ChatModel, spec string) (HarnessProfile, error) {
	return HarnessProfile{}, notimpl.Err("profiles.HarnessProfileFor")
}

// MergeHarnessProfiles layers override on base (`_merge_profiles`).
func MergeHarnessProfiles(base, override HarnessProfile) HarnessProfile {
	return override
}

// ApplyPrompt assembles basePrompt with the profile's BASE and SUFFIX slots.
func ApplyPrompt(p HarnessProfile, basePrompt string) string {
	return basePrompt
}
