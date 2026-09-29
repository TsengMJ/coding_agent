package llm

import "github.com/tsengmingchun/deepagents_golang/internal/notimpl"

// ModelSpec is a parsed "provider:model" string.
type ModelSpec struct {
	Provider string
	Model    string
}

// ProviderFactory constructs a ChatModel for a model name under one provider.
type ProviderFactory func(model string, opts map[string]any) (ChatModel, error)

// ParseModelSpec parses "provider:model" (e.g. "openai:gpt-5.5").
func ParseModelSpec(spec string) (ModelSpec, error) {
	return ModelSpec{}, notimpl.Err("llm.ParseModelSpec")
}

// RegisterProvider makes a provider available to InitChatModel.
func RegisterProvider(name string, factory ProviderFactory) error {
	return notimpl.Err("llm.RegisterProvider")
}

// InitChatModel builds a model from a "provider:model" spec (langchain `init_chat_model`).
func InitChatModel(spec string, opts map[string]any) (ChatModel, error) {
	return nil, notimpl.Err("llm.InitChatModel")
}

// ModelMatchesSpec reports whether model satisfies a "provider" or "provider:model" key.
func ModelMatchesSpec(model ChatModel, spec string) bool {
	return false
}

// ApproximateTokenCounter is the default character-based token estimate.
func ApproximateTokenCounter(messages []Message, tools []ToolSpec) int {
	return 0
}
