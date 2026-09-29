package llm

import (
	"context"
	"iter"
)

// ToolSpec is the model-facing description of a tool.
type ToolSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Parameters is a JSON Schema object describing the tool arguments.
	Parameters map[string]any `json:"parameters"`
}

// ResponseFormat requests provider-native structured output.
type ResponseFormat struct {
	Name   string         `json:"name"`
	Schema map[string]any `json:"schema"`
	Strict bool           `json:"strict,omitempty"`
}

// ChatRequest is one model invocation.
type ChatRequest struct {
	Messages       []Message
	Tools          []ToolSpec
	ToolChoice     string
	ResponseFormat *ResponseFormat
	// Settings holds per-call provider settings (temperature, reasoning effort, ...).
	Settings map[string]any
}

// Chunk is one streamed fragment of an assistant message.
type Chunk struct {
	Delta Message
	Done  bool
}

// ModelInfo identifies a constructed model (used for profile matching).
type ModelInfo struct {
	Provider string
	Model    string
}

// ModelProfile describes model capabilities (langchain `BaseChatModel.profile`).
type ModelProfile struct {
	MaxInputTokens  int
	MaxOutputTokens int
	ImageInputs     bool
	AudioInputs     bool
	VideoInputs     bool
	PDFInputs       bool
	// ToolMessage* report whether the block type is accepted inside tool results.
	ToolMessageImageInputs bool
	ToolMessagePDFInputs   bool
}

// ChatModel is the chat-model abstraction (langchain `BaseChatModel`).
type ChatModel interface {
	Info() ModelInfo
	Profile() ModelProfile
	Generate(ctx context.Context, req ChatRequest) (*Message, error)
	Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error]
}

// TokenCounter estimates the token count of messages (and optionally tools).
type TokenCounter func(messages []Message, tools []ToolSpec) int
