package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// DefaultOpenAIBaseURL is used when neither ProviderConfig.BaseURL nor
// OPENAI_BASE_URL is set.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

const errorBodyLimit = 2048

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

// OpenAIModel talks to the OpenAI Chat Completions API. The Responses API
// (useResponses) is not implemented yet.
//
// The same client serves OpenAI-compatible providers such as OpenRouter,
// which differ only in provider name, base URL and extra headers.
type OpenAIModel struct {
	cfg          ProviderConfig
	useResponses bool
	client       *http.Client
	provider     string
	headers      map[string]string
}

// NewOpenAIModel builds an OpenAI chat model; useResponses selects the Responses API.
func NewOpenAIModel(cfg ProviderConfig, useResponses bool) *OpenAIModel {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultOpenAIBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &OpenAIModel{cfg: cfg, useResponses: useResponses, client: http.DefaultClient, provider: "openai"}
}

func (m *OpenAIModel) Info() ModelInfo { return ModelInfo{Provider: m.provider, Model: m.cfg.Model} }

func (m *OpenAIModel) Profile() ModelProfile { return ModelProfile{} }

func (m *OpenAIModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return errSeq(notimpl.Err("llm.OpenAIModel.Stream"))
}

// Generate implements ChatModel via POST {BaseURL}/chat/completions.
func (m *OpenAIModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	if m.useResponses {
		return nil, notimpl.Err("llm.OpenAIModel.Generate(Responses API)")
	}
	if req.ResponseFormat != nil {
		return nil, notimpl.Err("llm.OpenAIModel.Generate(ResponseFormat)")
	}
	body, err := m.buildRequest(req)
	if err != nil {
		return nil, err
	}
	raw, err := m.post(ctx, "/chat/completions", body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.provider, err)
	}
	msg, err := parseChatCompletion(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.provider, err)
	}
	return msg, nil
}

type oaFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type oaToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function oaFunctionCall `json:"function"`
}

type oaMessage struct {
	Role       string       `json:"role"`
	Content    *string      `json:"content"`
	Name       string       `json:"name,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type oaTool struct {
	Type     string   `json:"type"`
	Function ToolSpec `json:"function"`
}

func (m *OpenAIModel) buildRequest(req ChatRequest) (map[string]any, error) {
	msgs := make([]oaMessage, 0, len(req.Messages))
	for i, msg := range req.Messages {
		om, err := toOpenAIMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("%s: message %d: %w", m.provider, i, err)
		}
		msgs = append(msgs, om)
	}
	body := map[string]any{}
	for k, v := range m.cfg.Settings {
		body[k] = v
	}
	for k, v := range req.Settings {
		body[k] = v
	}
	body["model"] = m.cfg.Model
	body["messages"] = msgs
	if len(req.Tools) > 0 {
		tools := make([]oaTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = oaTool{Type: "function", Function: t}
		}
		body["tools"] = tools
	}
	if req.ToolChoice != "" {
		body["tool_choice"] = req.ToolChoice
	}
	return body, nil
}

func toOpenAIMessage(msg Message) (oaMessage, error) {
	for _, c := range msg.Content {
		if c.Type != BlockText {
			return oaMessage{}, notimpl.Err("llm.OpenAIModel(" + string(c.Type) + " content)")
		}
	}
	om := oaMessage{Role: string(msg.Role), Name: msg.Name, ToolCallID: msg.ToolCallID}
	if msg.Role == RoleTool {
		om.Name = ""
	}
	text := msg.Text()
	if text != "" || len(msg.ToolCalls) == 0 {
		om.Content = &text
	}
	for _, tc := range msg.ToolCalls {
		args, err := json.Marshal(tc.Args)
		if err != nil {
			return oaMessage{}, fmt.Errorf("encode args of tool call %s: %w", tc.ID, err)
		}
		if tc.Args == nil {
			args = []byte("{}")
		}
		om.ToolCalls = append(om.ToolCalls, oaToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: oaFunctionCall{Name: tc.Name, Arguments: string(args)},
		})
	}
	return om, nil
}

func (m *OpenAIModel) post(ctx context.Context, path string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if m.cfg.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+m.cfg.APIKey)
	}
	for k, v := range m.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := m.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := raw
		if len(snippet) > errorBodyLimit {
			snippet = snippet[:errorBodyLimit]
		}
		return nil, fmt.Errorf("POST %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return raw, nil
}

type oaResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   *string      `json:"content"`
			Refusal   *string      `json:"refusal"`
			ToolCalls []oaToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

func parseChatCompletion(raw []byte) (*Message, error) {
	var resp oaResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("response has no choices")
	}
	choice := resp.Choices[0]
	msg := &Message{
		ID:       resp.ID,
		Role:     RoleAssistant,
		Metadata: map[string]any{"model": resp.Model, "finish_reason": choice.FinishReason},
	}
	if c := choice.Message.Content; c != nil && *c != "" {
		msg.Content = []ContentBlock{TextBlock(*c)}
	}
	if r := choice.Message.Refusal; r != nil && *r != "" {
		msg.Metadata["refusal"] = *r
	}
	for _, tc := range choice.Message.ToolCalls {
		args := map[string]any{}
		if strings.TrimSpace(tc.Function.Arguments) != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return nil, fmt.Errorf("decode args of tool call %s (%s): %w", tc.ID, tc.Function.Name, err)
			}
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	if u := resp.Usage; u != nil {
		msg.Usage = &Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens}
		if u.PromptTokensDetails != nil {
			msg.Usage.CacheRead = u.PromptTokensDetails.CachedTokens
		}
	}
	return msg, nil
}
