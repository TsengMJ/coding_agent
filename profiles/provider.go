package profiles

import (
	"github.com/tsengmingchun/quill/internal/notimpl"
	"github.com/tsengmingchun/quill/llm"
)

// ProviderProfile shapes how a model is constructed from a string spec.
type ProviderProfile struct {
	// InitKwargs are static options forwarded to llm.InitChatModel.
	InitKwargs map[string]any
	// PreInit runs before construction (e.g. dependency/version checks).
	PreInit func(spec string) error
	// InitKwargsFactory derives options at construction time (e.g. from env vars).
	InitKwargsFactory func() (map[string]any, error)
}

// RegisterProviderProfile registers p under key, merging additively.
func RegisterProviderProfile(key string, p ProviderProfile) error {
	return notimpl.Err("profiles.RegisterProviderProfile")
}

// ProviderProfileFor returns the merged provider profile for spec, or nil.
func ProviderProfileFor(spec string) (*ProviderProfile, error) {
	return nil, notimpl.Err("profiles.ProviderProfileFor")
}

// ResolveModel builds a model from a "provider:model" spec, applying the
// matching provider profile (`resolve_model`).
func ResolveModel(spec string) (llm.ChatModel, error) {
	return nil, notimpl.Err("profiles.ResolveModel")
}

// RegisterBuiltins registers the built-in provider profiles (nvidia, openai,
// openrouter) and harness profiles (Anthropic haiku-4.5 / sonnet-4.6 /
// opus-4.7, NVIDIA Nemotron 3 Ultra, OpenAI Codex).
func RegisterBuiltins() error {
	return notimpl.Err("profiles.RegisterBuiltins")
}
