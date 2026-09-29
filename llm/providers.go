package llm

import (
	"context"
	"iter"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
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

// OpenAIModel talks to the OpenAI Responses / Chat Completions API.
type OpenAIModel struct {
	cfg          ProviderConfig
	useResponses bool
}

// NewOpenAIModel builds an OpenAI chat model; useResponses selects the Responses API.
func NewOpenAIModel(cfg ProviderConfig, useResponses bool) *OpenAIModel {
	return &OpenAIModel{cfg: cfg, useResponses: useResponses}
}

func (m *OpenAIModel) Info() ModelInfo { return ModelInfo{Provider: "openai", Model: m.cfg.Model} }

func (m *OpenAIModel) Profile() ModelProfile { return ModelProfile{} }

func (m *OpenAIModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	return nil, notimpl.Err("llm.OpenAIModel.Generate")
}

func (m *OpenAIModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return errSeq(notimpl.Err("llm.OpenAIModel.Stream"))
}

func errSeq(err error) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) { yield(Chunk{}, err) }
}
