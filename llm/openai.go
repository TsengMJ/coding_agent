package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// DefaultOpenAIBaseURL is used when neither ProviderConfig.BaseURL nor
// OPENAI_BASE_URL is set.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

func init() {
	if err := RegisterProvider("openai", newOpenAIFromSpec); err != nil {
		panic(err)
	}
}

func newOpenAIFromSpec(model string, opts map[string]any) (ChatModel, error) {
	cfg := ProviderConfig{
		Model:    model,
		APIKey:   os.Getenv("OPENAI_API_KEY"),
		BaseURL:  os.Getenv("OPENAI_BASE_URL"),
		Settings: opts,
	}
	if cfg.APIKey == "" {
		return nil, errors.New("openai: OPENAI_API_KEY is not set")
	}
	return NewOpenAIModel(cfg, false), nil
}

// OpenAIModel talks to the OpenAI Chat Completions API through langchaingo.
// The Responses API (useResponses) is not implemented yet.
//
// The same client serves OpenAI-compatible providers such as OpenRouter,
// which differ only in provider name, base URL and extra headers.
type OpenAIModel struct {
	cfg          ProviderConfig
	useResponses bool
	provider     string
	headers      map[string]string

	once    sync.Once
	client  *openai.LLM
	initErr error
}

// NewOpenAIModel builds an OpenAI chat model; useResponses selects the Responses API.
func NewOpenAIModel(cfg ProviderConfig, useResponses bool) *OpenAIModel {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultOpenAIBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &OpenAIModel{cfg: cfg, useResponses: useResponses, provider: "openai"}
}

func (m *OpenAIModel) Info() ModelInfo { return ModelInfo{Provider: m.provider, Model: m.cfg.Model} }

func (m *OpenAIModel) Profile() ModelProfile { return ModelProfile{} }

// Generate implements ChatModel by calling langchaingo's OpenAI client.
func (m *OpenAIModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	return m.generate(ctx, req, nil)
}

// generate runs one completion; a non-nil sink requests a streamed response
// whose parsed tool calls replace langchaingo's.
func (m *OpenAIModel) generate(ctx context.Context, req ChatRequest, sink *streamSink) (*Message, error) {
	if m.useResponses {
		return nil, notimpl.Err("llm.OpenAIModel.Generate(Responses API)")
	}
	if req.ResponseFormat != nil {
		return nil, notimpl.Err("llm.OpenAIModel.Generate(ResponseFormat)")
	}
	client, err := m.llm()
	if err != nil {
		return nil, err
	}
	msgs := make([]llms.MessageContent, 0, len(req.Messages))
	for i, msg := range req.Messages {
		converted, err := toLangChainMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("%s: message %d: %w", m.provider, i, err)
		}
		msgs = append(msgs, converted)
	}
	tools := make([]llms.Tool, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name: t.Name, Description: t.Description, Parameters: t.Parameters,
			},
		}
	}
	opts := m.callOptions(req, tools)
	if sink != nil {
		opts = append(opts, llms.WithStreamingFunc(func(context.Context, []byte) error { return nil }))
	}
	resp, err := client.GenerateContent(ctx, msgs, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.provider, err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("%s: response has no choices", m.provider)
	}
	msg := fromLangChainChoice(resp.Choices[0])
	if sink != nil {
		if calls := sink.toolCalls(); calls != nil {
			msg.ToolCalls = calls
		}
	}
	return msg, nil
}

func (m *OpenAIModel) llm() (*openai.LLM, error) {
	m.once.Do(func() {
		opts := []openai.Option{
			openai.WithToken(m.cfg.APIKey),
			openai.WithModel(m.cfg.Model),
			openai.WithBaseURL(m.cfg.BaseURL),
			openai.WithHTTPClient(headerDoer{
				base:    retryDoer{base: modelHTTPClient(), maxRetries: m.maxRetries()},
				headers: m.headers,
			}),
		}
		m.client, m.initErr = openai.New(opts...)
	})
	return m.client, m.initErr
}

// maxRetries reads the "max_retries" setting, defaulting to DefaultMaxRetries.
func (m *OpenAIModel) maxRetries() int {
	if n, ok := asInt(m.cfg.Settings["max_retries"]); ok && n >= 0 {
		return n
	}
	return DefaultMaxRetries
}

func (m *OpenAIModel) callOptions(req ChatRequest, tools []llms.Tool) []llms.CallOption {
	opts := []llms.CallOption{llms.WithModel(m.cfg.Model)}
	if len(tools) > 0 {
		opts = append(opts, llms.WithTools(tools))
	}
	if req.ToolChoice != "" {
		opts = append(opts, llms.WithToolChoice(req.ToolChoice))
	}
	applySettings(&opts, m.cfg.Settings)
	applySettings(&opts, req.Settings)
	return opts
}

func applySettings(opts *[]llms.CallOption, settings map[string]any) {
	if settings == nil {
		return
	}
	if v, ok := number(settings["temperature"]); ok {
		*opts = append(*opts, llms.WithTemperature(v))
	}
	if v, ok := settings["max_tokens"]; ok {
		if n, ok := asInt(v); ok {
			*opts = append(*opts, llms.WithMaxTokens(n))
		}
	}
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func toLangChainMessage(msg Message) (llms.MessageContent, error) {
	for _, c := range msg.Content {
		if c.Type != BlockText {
			return llms.MessageContent{}, notimpl.Err("llm.OpenAIModel(" + string(c.Type) + " content)")
		}
	}
	if msg.Role == RoleTool {
		return llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{llms.ToolCallResponse{
				ToolCallID: msg.ToolCallID, Name: msg.Name, Content: msg.Text(),
			}},
		}, nil
	}
	role, err := langChainRole(msg.Role)
	if err != nil {
		return llms.MessageContent{}, err
	}
	var parts []llms.ContentPart
	if text := msg.Text(); text != "" || len(msg.ToolCalls) == 0 {
		parts = append(parts, llms.TextPart(text))
	}
	for _, tc := range msg.ToolCalls {
		args, err := json.Marshal(tc.Args)
		if err != nil {
			return llms.MessageContent{}, fmt.Errorf("encode args of tool call %s: %w", tc.ID, err)
		}
		if tc.Args == nil {
			args = []byte("{}")
		}
		parts = append(parts, llms.ToolCall{
			ID: tc.ID, Type: "function",
			FunctionCall: &llms.FunctionCall{Name: tc.Name, Arguments: string(args)},
		})
	}
	return llms.MessageContent{Role: role, Parts: parts}, nil
}

func langChainRole(role Role) (llms.ChatMessageType, error) {
	switch role {
	case RoleSystem:
		return llms.ChatMessageTypeSystem, nil
	case RoleUser:
		return llms.ChatMessageTypeHuman, nil
	case RoleAssistant:
		return llms.ChatMessageTypeAI, nil
	default:
		return "", fmt.Errorf("unsupported role %q", role)
	}
}

func fromLangChainChoice(choice *llms.ContentChoice) *Message {
	msg := &Message{
		Role:     RoleAssistant,
		Metadata: map[string]any{"finish_reason": choice.StopReason},
	}
	if choice.Content != "" {
		msg.Content = []ContentBlock{TextBlock(choice.Content)}
	}
	for _, tc := range choice.ToolCalls {
		if tc.FunctionCall == nil {
			continue
		}
		args := map[string]any{}
		if strings.TrimSpace(tc.FunctionCall.Arguments) != "" {
			_ = json.Unmarshal([]byte(tc.FunctionCall.Arguments), &args)
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.FunctionCall.Name, Args: args})
	}
	if info := choice.GenerationInfo; info != nil {
		in, _ := asInt(info["PromptTokens"])
		out, _ := asInt(info["CompletionTokens"])
		total, _ := asInt(info["TotalTokens"])
		cache, _ := asInt(info["PromptCachedTokens"])
		if in != 0 || out != 0 || total != 0 || cache != 0 {
			msg.Usage = &Usage{InputTokens: in, OutputTokens: out, TotalTokens: total, CacheRead: cache}
		}
	}
	return msg
}

// headerDoer adds provider headers and rewrites JSON null message content to
// an empty string. langchaingo unmarshals content into a Go string, which
// rejects the null OpenAI sends on tool-call messages. Event streams are not
// buffered; when a streamSink is in the context they go through sseTee under
// an idle watchdog.
type headerDoer struct {
	base    doer
	headers map[string]string
}

func (h headerDoer) Do(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		if req.Header.Get(k) == "" {
			req.Header.Set(k, v)
		}
	}
	base := h.base
	if base == nil {
		base = modelHTTPClient()
	}
	sink := streamSinkFrom(req.Context())
	var watch *idleWatch
	if sink != nil {
		watch = startIdleWatch(req.Context())
		req = req.WithContext(watch.ctx)
	}
	resp, err := base.Do(req)
	if watch != nil {
		if err != nil {
			if watch.ctx.Err() != nil {
				err = ctxError(watch.ctx)
			}
			watch.stop()
			return resp, err
		}
		resp.Body = &watchedBody{r: resp.Body, watch: watch}
	}
	if err != nil || resp == nil || resp.Body == nil || resp.StatusCode != http.StatusOK {
		return resp, err
	}
	if isEventStream(resp) {
		if sink != nil {
			resp.Body = &sseTee{r: resp.Body, sink: sink}
		}
		return resp, nil
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte(`"content": null`), []byte(`"content":""`))
	raw = bytes.ReplaceAll(raw, []byte(`"content":null`), []byte(`"content":""`))
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	return resp, nil
}
