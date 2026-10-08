package llm

import (
	"context"
	"iter"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
)

// ProviderConfig holds common construction options for built-in providers.
type ProviderConfig struct {
	Model   string
	APIKey  string
	BaseURL string
	// Settings are default per-call settings (temperature, max tokens, ...).
	Settings map[string]any
}

// AnthropicModel talks to the Anthropic Messages API.
type AnthropicModel struct{ cfg ProviderConfig }

// NewAnthropicModel builds an Anthropic chat model.
func NewAnthropicModel(cfg ProviderConfig) *AnthropicModel { return &AnthropicModel{cfg: cfg} }

func (m *AnthropicModel) Info() ModelInfo {
	return ModelInfo{Provider: "anthropic", Model: m.cfg.Model}
}

func (m *AnthropicModel) Profile() ModelProfile { return ModelProfile{} }

func (m *AnthropicModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	return nil, notimpl.Err("llm.AnthropicModel.Generate")
}

func (m *AnthropicModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return errSeq(notimpl.Err("llm.AnthropicModel.Stream"))
}

func errSeq(err error) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) { yield(Chunk{}, err) }
}
