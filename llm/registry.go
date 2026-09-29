package llm

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// ModelSpec is a parsed "provider:model" string.
type ModelSpec struct {
	Provider string
	Model    string
}

// ProviderFactory constructs a ChatModel for a model name under one provider.
type ProviderFactory func(model string, opts map[string]any) (ChatModel, error)

var (
	providersMu sync.RWMutex
	providers   = map[string]ProviderFactory{}
)

// ParseModelSpec parses "provider:model" (e.g. "openai:gpt-5.5"). Only the
// first colon separates the provider, so model names may contain colons.
func ParseModelSpec(spec string) (ModelSpec, error) {
	provider, model, ok := strings.Cut(spec, ":")
	if !ok || provider == "" || model == "" {
		return ModelSpec{}, fmt.Errorf("invalid model spec %q: want \"provider:model\"", spec)
	}
	return ModelSpec{Provider: provider, Model: model}, nil
}

// RegisterProvider makes a provider available to InitChatModel.
func RegisterProvider(name string, factory ProviderFactory) error {
	if name == "" || factory == nil {
		return fmt.Errorf("register provider: name and factory are required")
	}
	providersMu.Lock()
	defer providersMu.Unlock()
	if _, exists := providers[name]; exists {
		return fmt.Errorf("register provider: %q already registered", name)
	}
	providers[name] = factory
	return nil
}

// InitChatModel builds a model from a "provider:model" spec (langchain `init_chat_model`).
func InitChatModel(spec string, opts map[string]any) (ChatModel, error) {
	parsed, err := ParseModelSpec(spec)
	if err != nil {
		return nil, err
	}
	providersMu.RLock()
	factory, ok := providers[parsed.Provider]
	providersMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown model provider %q (registered: %s)",
			parsed.Provider, strings.Join(registeredProviders(), ", "))
	}
	return factory(parsed.Model, opts)
}

func registeredProviders() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// ModelMatchesSpec reports whether model satisfies a "provider" or "provider:model" key.
func ModelMatchesSpec(model ChatModel, spec string) bool {
	return false
}

// ApproximateTokenCounter is the default character-based token estimate.
func ApproximateTokenCounter(messages []Message, tools []ToolSpec) int {
	return 0
}
