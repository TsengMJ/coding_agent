package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
)

const (
	// DefaultAnthropicBaseURL is used when neither ProviderConfig.BaseURL
	// nor ANTHROPIC_BASE_URL is set.
	DefaultAnthropicBaseURL = "https://api.anthropic.com"
	anthropicVersion        = "2023-06-01"
	// defaultAnthropicMaxTokens fills the Messages API's required
	// max_tokens when neither the settings nor the profile give one.
	defaultAnthropicMaxTokens = 16384
	// maxCacheBreakpoints is the Messages API's limit per request.
	maxCacheBreakpoints = 4
)

func init() {
	if err := RegisterProvider("anthropic", newAnthropicFromSpec); err != nil {
		panic(err)
	}
}

func newAnthropicFromSpec(model string, opts map[string]any) (ChatModel, error) {
	cfg := ProviderConfig{
		Model:    model,
		APIKey:   os.Getenv("ANTHROPIC_API_KEY"),
		BaseURL:  os.Getenv("ANTHROPIC_BASE_URL"),
		Settings: opts,
	}
	if cfg.APIKey == "" {
		return nil, errors.New("anthropic: ANTHROPIC_API_KEY is not set")
	}
	return NewAnthropicModel(cfg), nil
}

// AnthropicModel talks to the Anthropic Messages API. Every call streams,
// so Generate gets the same idle watchdog as Stream.
//
// Leading system messages become the system prompt; later ones (such as
// the per-request <repair-state>) are sent as user text, which the API
// requires. Prompt caching marks the end of the system prompt and the last
// block before such a changing tail.
type AnthropicModel struct{ cfg ProviderConfig }

// NewAnthropicModel builds an Anthropic chat model.
func NewAnthropicModel(cfg ProviderConfig) *AnthropicModel {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultAnthropicBaseURL
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &AnthropicModel{cfg: cfg}
}

func (m *AnthropicModel) Info() ModelInfo {
	return ModelInfo{Provider: "anthropic", Model: m.cfg.Model}
}

// Profile comes from the built-in context-window table; Claude reads
// images in user messages and tool results.
func (m *AnthropicModel) Profile() ModelProfile {
	p, _ := KnownProfile(m.cfg.Model)
	p.ImageInputs, p.ToolMessageImageInputs = true, true
	return p
}

// Generate implements ChatModel by draining Stream.
func (m *AnthropicModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	for c, err := range m.Stream(ctx, req) {
		if err != nil {
			return nil, err
		}
		if c.Done {
			return &c.Delta, nil
		}
	}
	return nil, errors.New("anthropic: stream ended without a message")
}

// Stream implements ChatModel. Text deltas are yielded as they arrive; the
// final chunk (Done) carries the complete message.
func (m *AnthropicModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		body, err := m.open(ctx, req)
		if err != nil {
			yield(Chunk{}, err)
			return
		}
		defer body.Close()
		var reply anthropicReply
		r := bufio.NewReader(body)
		for {
			line, readErr := r.ReadBytes('\n')
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				text, done, err := reply.apply(bytes.TrimSpace(data))
				if err != nil {
					yield(Chunk{}, fmt.Errorf("anthropic: %w", err))
					return
				}
				if text != "" && !yield(Chunk{Delta: NewAssistantMessage(text)}, nil) {
					return
				}
				if done {
					yield(Chunk{Delta: reply.message(), Done: true}, nil)
					return
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					readErr = errors.New("stream ended before message_stop")
				}
				yield(Chunk{}, fmt.Errorf("anthropic: %w", readErr))
				return
			}
		}
	}
}

// open sends the request and returns the event stream under an idle
// watchdog; failed statuses become errors.
func (m *AnthropicModel) open(ctx context.Context, req ChatRequest) (io.ReadCloser, error) {
	payload, err := m.requestBody(req)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("anthropic: encode request: %w", err)
	}
	watch := startIdleWatch(ctx)
	httpReq, err := http.NewRequestWithContext(watch.ctx, http.MethodPost, m.cfg.BaseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		watch.stop()
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("x-api-key", m.cfg.APIKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	resp, err := retryDoer{base: modelHTTPClient(), maxRetries: m.maxRetries()}.Do(httpReq)
	if err != nil {
		if watch.ctx.Err() != nil {
			err = ctxError(watch.ctx)
		}
		watch.stop()
		return nil, fmt.Errorf("anthropic: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		watch.stop()
		return nil, anthropicStatusError(resp.StatusCode, msg)
	}
	return &watchedBody{r: resp.Body, watch: watch}, nil
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e anthropicError) String() string { return e.Type + ": " + e.Message }

func anthropicStatusError(status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	var parsed struct {
		Error *anthropicError `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != nil {
		detail = parsed.Error.String()
	}
	err := fmt.Errorf("anthropic: status %d: %s", status, detail)
	if isContextOverflow(err) {
		return fmt.Errorf("anthropic: %w: %w", ErrContextOverflow, err)
	}
	return err
}

func (m *AnthropicModel) maxRetries() int {
	if n, ok := asInt(m.cfg.Settings["max_retries"]); ok && n >= 0 {
		return n
	}
	return DefaultMaxRetries
}

// setting returns a per-call setting, else the model's default.
func (m *AnthropicModel) setting(req ChatRequest, key string) (any, bool) {
	if v, ok := req.Settings[key]; ok {
		return v, true
	}
	v, ok := m.cfg.Settings[key]
	return v, ok
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func (m *AnthropicModel) requestBody(req ChatRequest) (map[string]any, error) {
	if req.ResponseFormat != nil {
		return nil, notimpl.Err("llm.AnthropicModel.Generate(ResponseFormat)")
	}
	var system []map[string]any
	var msgs []anthropicMessage
	// stable is the last block that is not a later system message, where
	// the moving cache breakpoint goes.
	var stable map[string]any
	leading := true
	for i, msg := range req.Messages {
		if msg.Role == RoleSystem && leading {
			blocks, err := anthropicUserBlocks(msg)
			if err != nil {
				return nil, fmt.Errorf("anthropic: message %d: %w", i, err)
			}
			system = append(system, blocks...)
			continue
		}
		leading = false
		role, blocks, err := anthropicBlocks(msg)
		if err != nil {
			return nil, fmt.Errorf("anthropic: message %d: %w", i, err)
		}
		if len(blocks) == 0 {
			continue
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
		} else {
			msgs = append(msgs, anthropicMessage{Role: role, Content: blocks})
		}
		if msg.Role != RoleSystem {
			stable = blocks[len(blocks)-1]
		}
	}
	marks := countCacheMarks(system) + countCacheMarks(slices.Concat(contentOf(msgs)...))
	for _, b := range []map[string]any{lastBlock(system), stable} {
		if b != nil && b["cache_control"] == nil && marks < maxCacheBreakpoints {
			b["cache_control"] = map[string]any{"type": "ephemeral"}
			marks++
		}
	}

	maxTokens := m.Profile().MaxOutputTokens
	if v, ok := m.setting(req, "max_tokens"); ok {
		if n, ok := asInt(v); ok {
			maxTokens = n
		}
	}
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxTokens
	}
	body := map[string]any{
		"model":      m.cfg.Model,
		"max_tokens": maxTokens,
		"messages":   msgs,
		"stream":     true,
	}
	if len(system) > 0 {
		body["system"] = system
	}
	if v, ok := m.setting(req, "temperature"); ok {
		if t, ok := number(v); ok {
			body["temperature"] = t
		}
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, len(req.Tools))
		for i, t := range req.Tools {
			schema := t.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": schema}
		}
		body["tools"] = tools
	}
	switch req.ToolChoice {
	case "":
	case "auto", "none":
		body["tool_choice"] = map[string]any{"type": req.ToolChoice}
	case "any", "required":
		body["tool_choice"] = map[string]any{"type": "any"}
	default:
		body["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice}
	}
	return body, nil
}

func contentOf(msgs []anthropicMessage) [][]map[string]any {
	out := make([][]map[string]any, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func lastBlock(blocks []map[string]any) map[string]any {
	if len(blocks) == 0 {
		return nil
	}
	return blocks[len(blocks)-1]
}

func countCacheMarks(blocks []map[string]any) int {
	n := 0
	for _, b := range blocks {
		if b["cache_control"] != nil {
			n++
		}
	}
	return n
}

// anthropicBlocks converts one message after the system prompt into the
// role and content blocks it contributes.
func anthropicBlocks(msg Message) (string, []map[string]any, error) {
	switch msg.Role {
	case RoleUser, RoleSystem:
		blocks, err := anthropicUserBlocks(msg)
		return "user", blocks, err
	case RoleTool:
		content, err := anthropicUserBlocks(msg)
		if err != nil {
			return "", nil, err
		}
		result := map[string]any{"type": "tool_result", "tool_use_id": msg.ToolCallID}
		if len(content) > 0 {
			result["content"] = content
		}
		if msg.Status == ToolStatusError {
			result["is_error"] = true
		}
		return "user", []map[string]any{result}, nil
	case RoleAssistant:
		var blocks []map[string]any
		for _, c := range msg.Content {
			switch c.Type {
			case BlockText:
				if c.Text != "" {
					blocks = append(blocks, withCacheControl(map[string]any{"type": "text", "text": c.Text}, c))
				}
			case BlockReasoning:
				// Thinking blocks need the signature Anthropic sent with
				// them, which Message does not keep; they are dropped.
			default:
				return "", nil, notimpl.Err("llm.AnthropicModel(assistant " + string(c.Type) + " content)")
			}
		}
		for _, tc := range msg.ToolCalls {
			input := tc.Args
			if input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input})
		}
		return "assistant", blocks, nil
	default:
		return "", nil, fmt.Errorf("unsupported role %q", msg.Role)
	}
}

// anthropicUserBlocks converts text and image blocks; the API rejects
// empty text blocks, so they are left out.
func anthropicUserBlocks(msg Message) ([]map[string]any, error) {
	var blocks []map[string]any
	for _, c := range msg.Content {
		switch c.Type {
		case BlockText:
			if c.Text != "" {
				blocks = append(blocks, withCacheControl(map[string]any{"type": "text", "text": c.Text}, c))
			}
		case BlockImage:
			source, err := anthropicImageSource(c)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, withCacheControl(map[string]any{"type": "image", "source": source}, c))
		default:
			return nil, notimpl.Err("llm.AnthropicModel(" + string(msg.Role) + " " + string(c.Type) + " content)")
		}
	}
	return blocks, nil
}

func anthropicImageSource(c ContentBlock) (map[string]any, error) {
	mime, data := c.MimeType, c.Base64
	if data == "" {
		if rest, ok := strings.CutPrefix(c.URL, "data:"); ok {
			var found bool
			if mime, data, found = strings.Cut(rest, ";base64,"); !found {
				return nil, errors.New("image data URL is not base64")
			}
		} else if c.URL != "" {
			return map[string]any{"type": "url", "url": c.URL}, nil
		}
	}
	if data == "" {
		return nil, errors.New("image block without data or url")
	}
	if mime == "" {
		return nil, errors.New("image block without mime_type")
	}
	return map[string]any{"type": "base64", "media_type": mime, "data": data}, nil
}

func withCacheControl(block map[string]any, c ContentBlock) map[string]any {
	if c.CacheControl != nil {
		block["cache_control"] = c.CacheControl
	}
	return block
}

type anthropicUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

type anthropicEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *struct {
		Type string `json:"type"`
		Text string `json:"text"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *anthropicError `json:"error"`
}

type anthropicPart struct {
	typ, id, name string
	text, input   strings.Builder
}

// anthropicReply assembles a streamed message from its events.
type anthropicReply struct {
	parts      map[int]*anthropicPart
	usage      anthropicUsage
	stopReason string
}

// apply folds one event in, returning text to stream and whether the
// message is complete.
func (r *anthropicReply) apply(data []byte) (text string, done bool, err error) {
	var ev anthropicEvent
	if err := json.Unmarshal(data, &ev); err != nil {
		return "", false, fmt.Errorf("bad stream event: %w", err)
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			r.usage = ev.Message.Usage
		}
	case "content_block_start":
		if ev.ContentBlock != nil {
			p := r.part(ev.Index)
			p.typ, p.id, p.name = ev.ContentBlock.Type, ev.ContentBlock.ID, ev.ContentBlock.Name
			p.text.WriteString(ev.ContentBlock.Text)
			text = ev.ContentBlock.Text
		}
	case "content_block_delta":
		if ev.Delta == nil {
			break
		}
		p := r.part(ev.Index)
		switch ev.Delta.Type {
		case "text_delta":
			p.text.WriteString(ev.Delta.Text)
			text = ev.Delta.Text
		case "thinking_delta":
			p.text.WriteString(ev.Delta.Thinking)
		case "input_json_delta":
			p.input.WriteString(ev.Delta.PartialJSON)
		}
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			r.stopReason = ev.Delta.StopReason
		}
		if u := ev.Usage; u != nil {
			r.usage.OutputTokens = u.OutputTokens
			if u.InputTokens != 0 || u.CacheRead != 0 || u.CacheCreation != 0 {
				r.usage.InputTokens, r.usage.CacheRead, r.usage.CacheCreation = u.InputTokens, u.CacheRead, u.CacheCreation
			}
		}
	case "message_stop":
		return "", true, nil
	case "error":
		if ev.Error != nil {
			return "", false, errors.New(ev.Error.String())
		}
		return "", false, errors.New("stream error")
	}
	return text, false, nil
}

func (r *anthropicReply) part(i int) *anthropicPart {
	if r.parts == nil {
		r.parts = map[int]*anthropicPart{}
	}
	p := r.parts[i]
	if p == nil {
		p = &anthropicPart{}
		r.parts[i] = p
	}
	return p
}

func (r *anthropicReply) message() Message {
	msg := Message{Role: RoleAssistant, Metadata: map[string]any{"finish_reason": r.stopReason}}
	idx := make([]int, 0, len(r.parts))
	for i := range r.parts {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	for _, i := range idx {
		p := r.parts[i]
		switch p.typ {
		case "text":
			if s := p.text.String(); s != "" {
				msg.Content = append(msg.Content, TextBlock(s))
			}
		case "thinking":
			if s := p.text.String(); s != "" {
				msg.Content = append(msg.Content, ContentBlock{Type: BlockReasoning, Text: s})
			}
		case "tool_use":
			args := map[string]any{}
			if raw := strings.TrimSpace(p.input.String()); raw != "" {
				_ = json.Unmarshal([]byte(raw), &args)
			}
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: p.id, Name: p.name, Args: args})
		}
	}
	u := r.usage
	in := u.InputTokens + u.CacheRead + u.CacheCreation
	msg.Usage = &Usage{InputTokens: in, OutputTokens: u.OutputTokens, TotalTokens: in + u.OutputTokens, CacheRead: u.CacheRead, CacheWrite: u.CacheCreation}
	return msg
}
