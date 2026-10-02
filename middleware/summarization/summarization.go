// Package summarization compacts long conversations: it truncates old tool
// arguments, summarizes evicted history, offloads it to the backend and
// recovers from context-overflow errors (Python `summarization.py`).
//
// Stored messages are never rewritten. A summarization records an Event
// (cutoff index + summary message) in state, and every later model call sees
// the summary followed by the messages after the cutoff.
package summarization

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

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

// DefaultHistoryPathPrefix is where evicted history is offloaded.
const DefaultHistoryPathPrefix = "/conversation_history"

// Defaults for TruncateArgsSettings.
const (
	DefaultMaxArgLength   = 2000
	DefaultTruncationText = "...(argument truncated)"
)

// summarySourceKey marks summary messages in Message.Metadata
// (langchain `additional_kwargs["lc_source"] = "summarization"`).
const summarySourceKey = "lc_source"

// ErrNothingToCompact is returned by Compact when the history is already
// within the keep policy.
var ErrNothingToCompact = errors.New("summarization: nothing to compact")

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

// TruncateArgsSettings controls pre-summarization clipping of old
// write_file / edit_file arguments. A nil Trigger disables it.
type TruncateArgsSettings struct {
	Trigger        *ContextSize
	Keep           ContextSize
	MaxLength      int
	TruncationText string
}

// Event records a completed summarization (stored under EventStateKey).
// CutoffIndex is an index into the state's messages.
type Event struct {
	CutoffIndex    int         `json:"cutoff_index"`
	SummaryMessage llm.Message `json:"summary_message"`
	FilePath       string      `json:"file_path,omitempty"`
}

// Defaults are model-aware settings (`compute_summarization_defaults`).
type Defaults struct {
	Trigger              ContextSize
	Keep                 ContextSize
	TruncateArgsSettings TruncateArgsSettings
}

// Options configures the middleware; the trigger list has OR semantics.
type Options struct {
	// Model writes summaries; nil uses the model of the intercepted request.
	Model   llm.ChatModel
	Backend backend.Backend
	Trigger []TriggerClause
	Keep    ContextSize
	// TokenCounter defaults to llm.ApproximateTokenCounter.
	TokenCounter llm.TokenCounter
	// SummaryPrompt must contain "{messages}"; empty uses DefaultSummaryPrompt.
	SummaryPrompt string
	// TrimTokensToSummarize further caps each summary request; 0 uses the
	// model-aware default. Long history is summarized in bounded chunks.
	TrimTokensToSummarize int
	// DynamicDefaults recomputes the default trigger and keep policy for the
	// model selected for this run. Explicit New options leave this disabled.
	DynamicDefaults bool
	TruncateArgs    *TruncateArgsSettings
	// HistoryPathPrefix defaults to DefaultHistoryPathPrefix.
	HistoryPathPrefix string
}

// Middleware is the Deep Agents summarization middleware.
type Middleware struct {
	opts Options
}

// New builds the middleware with explicit options.
func New(opts Options) (*Middleware, error) {
	if opts.Backend == nil {
		return nil, errors.New("summarization.New: Backend is required")
	}
	if opts.Keep.Kind == "" {
		return nil, errors.New("summarization.New: Keep is required")
	}
	if opts.TokenCounter == nil {
		opts.TokenCounter = llm.ApproximateTokenCounter
	}
	if opts.SummaryPrompt == "" {
		opts.SummaryPrompt = DefaultSummaryPrompt
	}
	if !strings.Contains(opts.SummaryPrompt, "{messages}") {
		return nil, errors.New("summarization.New: SummaryPrompt must contain {messages}")
	}
	if opts.HistoryPathPrefix == "" {
		opts.HistoryPathPrefix = DefaultHistoryPathPrefix
	}
	opts.HistoryPathPrefix = strings.TrimRight(opts.HistoryPathPrefix, "/")
	if t := opts.TruncateArgs; t != nil {
		c := *t
		if c.MaxLength <= 0 {
			c.MaxLength = DefaultMaxArgLength
		}
		if c.TruncationText == "" {
			c.TruncationText = DefaultTruncationText
		}
		if c.Keep.Kind == "" {
			c.Keep = ContextSize{SizeMessages, 20}
		}
		opts.TruncateArgs = &c
	}
	return &Middleware{opts: opts}, nil
}

// NewForModel builds the middleware with model-aware defaults
// (`create_summarization_middleware`). Summaries are written by model.
func NewForModel(model llm.ChatModel, b backend.Backend) (*Middleware, error) {
	opts, err := DefaultOptions(model, b)
	if err != nil {
		return nil, err
	}
	return New(opts)
}

// DefaultOptions are the options NewForModel uses, for callers that adjust
// a field (such as HistoryPathPrefix) before calling New.
func DefaultOptions(model llm.ChatModel, b backend.Backend) (Options, error) {
	d, err := ComputeDefaults(model)
	if err != nil {
		return Options{}, err
	}
	return Options{
		Model:           model,
		Backend:         b,
		Trigger:         []TriggerClause{{d.Trigger}},
		Keep:            d.Keep,
		TruncateArgs:    &d.TruncateArgsSettings,
		DynamicDefaults: true,
	}, nil
}

// ComputeDefaults derives trigger/keep from the model profile: fractions of
// the context window when it is known, conservative fixed sizes otherwise.
func ComputeDefaults(model llm.ChatModel) (Defaults, error) {
	if model == nil {
		return Defaults{}, errors.New("summarization.ComputeDefaults: model is required")
	}
	if model.Profile().MaxInputTokens > 0 {
		trigger := ContextSize{SizeFraction, 0.80}
		return Defaults{
			Trigger:              trigger,
			Keep:                 ContextSize{SizeFraction, 0.10},
			TruncateArgsSettings: TruncateArgsSettings{Trigger: &trigger, Keep: ContextSize{SizeFraction, 0.10}},
		}, nil
	}
	trigger := ContextSize{SizeMessages, 20}
	return Defaults{
		Trigger:              ContextSize{SizeTokens, 170000},
		Keep:                 ContextSize{SizeMessages, 6},
		TruncateArgsSettings: TruncateArgsSettings{Trigger: &trigger, Keep: ContextSize{SizeMessages, 20}},
	}, nil
}

// Name keeps the public alias so profiles can exclude it by name.
func (m *Middleware) Name() string { return "SummarizationMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{
		EventStateKey:     {Name: EventStateKey, Private: true},
		SessionIDStateKey: {Name: SessionIDStateKey, Private: true},
	}
}

// WrapModelCall sends the effective history (summary + messages after the
// cutoff), clipping old tool arguments and summarizing when a trigger fires.
// A context-overflow error from the model forces one summarize-and-retry.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	active := llm.ModelForContext(ctx, req.Model)
	m = m.forModel(active)
	event := eventFrom(req.State)
	effective := applyEvent(req.Messages, event)
	specs := toolSpecs(req.Tools)
	if fixed := m.count(req.SystemMessage, nil, specs); fixed >= contextBudget(active) {
		return nil, fmt.Errorf("%w: system prompt and tool definitions use %d tokens, leaving no safe input budget", llm.ErrContextOverflow, fixed)
	}
	total := m.count(req.SystemMessage, effective, specs)
	msgs, clipped := m.truncateArgs(effective, total, active)
	if clipped {
		total = m.count(req.SystemMessage, msgs, specs)
	}

	var overflow error
	if !m.shouldSummarize(msgs, total, active) {
		resp, err := next(ctx, withMessages(req, msgs))
		if err == nil || !errors.Is(err, llm.ErrContextOverflow) {
			return resp, err
		}
		overflow = err
	}

	upd, summarized, err := m.summarize(ctx, req.State, event, msgs, active)
	if err != nil {
		return nil, err
	}
	if upd == nil {
		if overflow != nil {
			return nil, overflow
		}
		return next(ctx, withMessages(req, msgs))
	}
	resp, err := next(ctx, withMessages(req, summarized))
	if err != nil {
		if errors.Is(err, llm.ErrContextOverflow) {
			return nil, fmt.Errorf("%w: compacted request still exceeds the selected model window", err)
		}
		return nil, err
	}
	return withUpdate(resp, upd), nil
}

// Compact summarizes the thread now, regardless of triggers, and returns the
// state update to persist. It returns ErrNothingToCompact when the keep
// policy leaves nothing to summarize.
func (m *Middleware) Compact(ctx context.Context, state *runtime.State, model llm.ChatModel) (*runtime.Update, error) {
	model = llm.ModelForContext(ctx, model)
	m = m.forModel(model)
	event := eventFrom(state)
	upd, _, err := m.summarize(ctx, state, event, applyEvent(state.Messages, event), model)
	if err != nil {
		return nil, err
	}
	if upd == nil {
		return nil, ErrNothingToCompact
	}
	return upd, nil
}

func (m *Middleware) forModel(model llm.ChatModel) *Middleware {
	if !m.opts.DynamicDefaults {
		return m
	}
	d, err := ComputeDefaults(model)
	if err != nil {
		return m
	}
	copy := *m
	copy.opts = m.opts
	copy.opts.Trigger = []TriggerClause{{d.Trigger}}
	copy.opts.Keep = d.Keep
	copy.opts.TruncateArgs = &d.TruncateArgsSettings
	return &copy
}

// assumedContextWindow stands in for models without a known window; the
// fallback trigger of 170k tokens is 85% of it.
const assumedContextWindow = 200_000

func contextWindow(model llm.ChatModel) int {
	if model != nil && model.Profile().MaxInputTokens > 0 {
		return model.Profile().MaxInputTokens
	}
	return assumedContextWindow
}

func contextBudget(model llm.ChatModel) int {
	return max(contextWindow(model)*80/100, 1)
}

// summarize offloads and summarizes effective[:cutoff]. It returns a nil
// update when the keep policy selects nothing.
func (m *Middleware) summarize(ctx context.Context, state *runtime.State, event *Event, effective []llm.Message, model llm.ChatModel) (*runtime.Update, []llm.Message, error) {
	cutoff := m.cutoffIndex(effective, model)
	if cutoff <= 0 {
		return nil, nil, nil
	}
	evicted, preserved := effective[:cutoff], effective[cutoff:]
	session := sessionID(state)
	rc := backend.NewRunContext(state)
	path := m.offload(backend.WithRunContext(ctx, rc), evicted, session)
	summary, err := m.createSummary(ctx, evicted, model)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, llm.ErrContextOverflow) {
			return nil, nil, fmt.Errorf("summarization: %w", err)
		}
		// The evicted history is already offloaded, so the run can go on
		// without a model-written summary.
		log.Printf("summarization: summary model failed, continuing with the offload note: %v", err)
		summary = fmt.Sprintf("Summary unavailable: the summary model failed (%s). "+
			"The earlier conversation was moved out of context; read the saved history with read_file if you need details from it.",
			clipText(err.Error(), 300))
	}
	summaryMsg := summaryMessage(summary, path)
	stateCutoff := cutoff
	if event != nil {
		// effective[0] is the previous summary, not a state message.
		stateCutoff = event.CutoffIndex + cutoff - 1
	}
	upd := &runtime.Update{Values: map[string]any{
		EventStateKey:     Event{CutoffIndex: stateCutoff, SummaryMessage: summaryMsg, FilePath: path},
		SessionIDStateKey: session,
	}}
	if files := rc.FilesUpdate(); files != nil {
		upd.Values[backend.FilesStateKey] = files
	}
	return upd, append([]llm.Message{summaryMsg}, preserved...), nil
}

func (m *Middleware) createSummary(ctx context.Context, msgs []llm.Message, model llm.ChatModel) (string, error) {
	if m.opts.Model != nil {
		model = m.opts.Model
	}
	if model == nil {
		return "", errors.New("no model to write the summary")
	}
	// Chunks run one after another, each carrying the previous summary.
	budget := max(contextWindow(llm.ModelForContext(ctx, model))/4, 1)
	if m.opts.TrimTokensToSummarize > 0 {
		budget = min(budget, m.opts.TrimTokensToSummarize)
	}
	history := []rune(formatMessages(msgs))
	var summary string
	for start := 0; start < len(history); {
		prior := summary
		// Keep the previous summary within one quarter of the next input.
		if runes := []rune(prior); len(runes) > budget/4 {
			half := max(budget/8-40, 1)
			prior = string(runes[:half]) + "\n[Middle of summary omitted; full history is offloaded.]\n" + string(runes[len(runes)-half:])
		}
		prefix := ""
		if prior != "" {
			prefix = "<previous_summary>\n" + prior + "\n</previous_summary>\n"
		}
		remaining := len(history) - start
		low, high := 0, remaining
		for low < high {
			mid := low + (high-low+1)/2
			if summaryInputCost(m.summaryPrompt(prefix+string(history[start:start+mid]))) <= budget {
				low = mid
			} else {
				high = mid - 1
			}
		}
		if low == 0 {
			return "", fmt.Errorf("%w: summary instructions leave no room for conversation history", llm.ErrContextOverflow)
		}
		length := low
		var reply *llm.Message
		var err error
		for attempt := 0; attempt < 4; attempt++ {
			prompt := m.summaryPrompt(prefix + string(history[start:start+length]))
			reply, err = model.Generate(ctx, llm.ChatRequest{Messages: []llm.Message{llm.NewUserMessage(prompt)}})
			if !errors.Is(err, llm.ErrContextOverflow) {
				break
			}
			length /= 2
			if length == 0 {
				break
			}
		}
		if err != nil {
			return "", err
		}
		if reply == nil || strings.TrimSpace(reply.Text()) == "" {
			return "", errors.New("summary model returned no text")
		}
		summary = strings.TrimSpace(reply.Text())
		start += length
	}
	return summary, nil
}

func (m *Middleware) summaryPrompt(body string) string {
	return strings.TrimRight(strings.Replace(m.opts.SummaryPrompt, "{messages}", body, 1), " \n")
}

// summaryInputCost is conservative for non-ASCII text, which the general
// character/4 estimator undercounts substantially.
func summaryInputCost(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + 2*other + 4
}

// offload appends evicted messages (minus earlier summaries) to the session
// history file. Failure is not fatal: it returns "" and the summary then
// carries no file reference.
func (m *Middleware) offload(ctx context.Context, msgs []llm.Message, session string) string {
	path := m.opts.HistoryPathPrefix + "/" + session + ".md"
	var kept []llm.Message
	for _, msg := range msgs {
		if !isSummaryMessage(msg) {
			kept = append(kept, msg)
		}
	}
	section := fmt.Sprintf("## Summarized at %s\n\n%s\n\n", time.Now().UTC().Format(time.RFC3339), formatMessages(kept))
	existing := ""
	if res := m.opts.Backend.DownloadFiles(ctx, []string{path}); len(res) == 1 && res[0].Error == "" {
		existing = string(res[0].Content)
	}
	res := m.opts.Backend.UploadFiles(ctx, []backend.FileUpload{{Path: path, Content: []byte(existing + section)}})
	if len(res) != 1 || res[0].Error != "" {
		return ""
	}
	return path
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func summaryMessage(summary, path string) llm.Message {
	text := "Here is a summary of the conversation to date:\n\n" + summary
	if path != "" {
		text = "You are in the middle of a conversation that has been summarized.\n\n" +
			"The full conversation history has been saved to " + path + " should you need to refer back to it for details.\n\n" +
			"A condensed summary follows:\n\n<summary>\n" + summary + "\n</summary>"
	}
	msg := llm.NewUserMessage(text)
	msg.Metadata = map[string]any{summarySourceKey: "summarization"}
	return msg
}

func isSummaryMessage(msg llm.Message) bool {
	return msg.Role == llm.RoleUser && msg.Metadata[summarySourceKey] == "summarization"
}

// formatMessages renders messages for the summary prompt and the history
// file (langchain `get_buffer_string(format="xml")`, reduced).
func formatMessages(msgs []llm.Message) string {
	var b strings.Builder
	for i, msg := range msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "<%s>", msg.Role)
		b.WriteString(msg.Text())
		for _, call := range msg.ToolCalls {
			args, _ := json.Marshal(call.Args)
			fmt.Fprintf(&b, "\n<tool_call name=%q id=%q>%s</tool_call>", call.Name, call.ID, args)
		}
		fmt.Fprintf(&b, "</%s>", msg.Role)
	}
	return b.String()
}

func sessionID(state *runtime.State) string {
	if state != nil {
		if s, ok := state.Values[SessionIDStateKey].(string); ok && s != "" {
			return s
		}
	}
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return "session_" + hex.EncodeToString(buf[:])
}

// eventFrom reads the stored event, accepting its JSON checkpoint form.
func eventFrom(state *runtime.State) *Event {
	if state == nil {
		return nil
	}
	switch v := state.Values[EventStateKey].(type) {
	case nil:
		return nil
	case Event:
		return &v
	case *Event:
		return v
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		var e Event
		if json.Unmarshal(raw, &e) != nil {
			return nil
		}
		return &e
	}
}

// EffectiveMessages is msgs as the model sees them under state's
// summarization event: the summary, then the messages after the cutoff.
func EffectiveMessages(msgs []llm.Message, state *runtime.State) []llm.Message {
	return applyEvent(msgs, eventFrom(state))
}

// applyEvent returns the summary followed by messages after the cutoff.
func applyEvent(msgs []llm.Message, event *Event) []llm.Message {
	if event == nil {
		return slices.Clone(msgs)
	}
	out := []llm.Message{event.SummaryMessage}
	if event.CutoffIndex < len(msgs) {
		out = append(out, msgs[event.CutoffIndex:]...)
	}
	return out
}

func (m *Middleware) count(sys *llm.Message, msgs []llm.Message, specs []llm.ToolSpec) int {
	if sys != nil {
		msgs = append([]llm.Message{*sys}, msgs...)
	}
	return m.opts.TokenCounter(msgs, specs)
}

func toolSpecs(tools []tool.Tool) []llm.ToolSpec {
	specs := make([]llm.ToolSpec, len(tools))
	for i, t := range tools {
		specs[i] = tool.Spec(t)
	}
	return specs
}

func withMessages(req *agent.ModelRequest, msgs []llm.Message) *agent.ModelRequest {
	out := *req
	out.Messages = msgs
	return &out
}

func withUpdate(resp *agent.ModelResponse, upd *runtime.Update) *agent.ModelResponse {
	out := *resp
	if out.Update == nil {
		out.Update = upd
		return &out
	}
	merged := *out.Update
	merged.Values = map[string]any{}
	for k, v := range out.Update.Values {
		merged.Values[k] = v
	}
	for k, v := range upd.Values {
		merged.Values[k] = v
	}
	out.Update = &merged
	return &out
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
