// Package summarization compacts long conversations: it truncates old tool
// arguments, summarizes evicted history, offloads it to the backend and
// recovers from context-overflow errors (Python `summarization.py`).
package summarization

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/internal/stubtool"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Private state keys.
const (
	EventStateKey     = "_summarization_event"
	SessionIDStateKey = "_summarization_session_id"
)

// CompactToolName is the tool exposed by ToolMiddleware.
const CompactToolName = "compact_conversation"

// SizeKind is the unit of a ContextSize.
type SizeKind string

const (
	SizeTokens   SizeKind = "tokens"
	SizeMessages SizeKind = "messages"
	SizeFraction SizeKind = "fraction"
)

// ContextSize is a threshold such as {tokens, 100000} or {fraction, 0.85}.
type ContextSize struct {
	Kind  SizeKind
	Value float64
}

// TriggerClause combines thresholds with AND semantics.
type TriggerClause []ContextSize

// TruncateArgsSettings controls pre-summarization clipping of old tool args.
type TruncateArgsSettings struct {
	Trigger        *ContextSize
	Keep           ContextSize
	MaxLength      int
	TruncationText string
}

// Event records a completed summarization (stored under EventStateKey).
type Event struct {
	CutoffIndex    int
	SummaryMessage llm.Message
	FilePath       string
}

// Defaults are model-aware settings (`compute_summarization_defaults`).
type Defaults struct {
	Trigger              ContextSize
	Keep                 ContextSize
	TruncateArgsSettings TruncateArgsSettings
}

// Options configures the middleware; the trigger list has OR semantics.
type Options struct {
	Model                 llm.ChatModel
	Backend               backend.Backend
	Trigger               []TriggerClause
	Keep                  ContextSize
	TokenCounter          llm.TokenCounter
	SummaryPrompt         string
	TrimTokensToSummarize int
	TruncateArgs          *TruncateArgsSettings
	// HistoryPathPrefix defaults to "/conversation_history/".
	HistoryPathPrefix string
}

// Middleware is the Deep Agents summarization middleware.
type Middleware struct {
	opts Options
}

// New builds the middleware with explicit options.
func New(opts Options) *Middleware { return &Middleware{opts: opts} }

// NewForModel builds the middleware with model-aware defaults
// (`create_summarization_middleware`).
func NewForModel(model llm.ChatModel, b backend.Backend) (*Middleware, error) {
	return nil, notimpl.Err("summarization.NewForModel")
}

// ComputeDefaults derives trigger/keep from the model profile.
func ComputeDefaults(model llm.ChatModel) (Defaults, error) {
	return Defaults{}, notimpl.Err("summarization.ComputeDefaults")
}

// Name keeps the public alias so profiles can exclude it by name.
func (m *Middleware) Name() string { return "SummarizationMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{
		EventStateKey:     {Name: EventStateKey, Private: true},
		SessionIDStateKey: {Name: SessionIDStateKey, Private: true},
	}
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("summarization.Middleware.WrapModelCall")
}

// ToolMiddleware exposes manual compaction as the compact_conversation tool
// (`SummarizationToolMiddleware`).
type ToolMiddleware struct {
	summarizer *Middleware
}

// NewToolMiddleware wraps a summarization middleware with the compact tool.
func NewToolMiddleware(summarizer *Middleware) *ToolMiddleware {
	return &ToolMiddleware{summarizer: summarizer}
}

func (m *ToolMiddleware) Name() string { return "SummarizationToolMiddleware" }

func (m *ToolMiddleware) Tools() []tool.Tool {
	return []tool.Tool{stubtool.New(CompactToolName, "")}
}

func (m *ToolMiddleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("summarization.ToolMiddleware.WrapModelCall")
}
