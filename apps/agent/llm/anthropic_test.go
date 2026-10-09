package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// anthropicEvents is a streamed Messages API reply: "event: data" pairs.
type anthropicEvents [][2]string

func (e anthropicEvents) write(w io.Writer) {
	for _, ev := range e {
		_, _ = io.WriteString(w, "event: "+ev[0]+"\ndata: "+ev[1]+"\n\n")
	}
}

var anthropicHello = anthropicEvents{
	{"message_start", `{"type":"message_start","message":{"id":"msg_1","role":"assistant","content":[],"usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}`},
	{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
	{"ping", `{"type":"ping"}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`},
	{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`},
	{"content_block_stop", `{"type":"content_block_stop","index":0}`},
	{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`},
	{"message_stop", `{"type":"message_stop"}`},
}

// fakeAnthropic answers each request with the next status; 200 streams
// events. The last request body is captured.
func fakeAnthropic(t *testing.T, events anthropicEvents, captured *map[string]any, statuses ...int) (*AnthropicModel, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "sk-ant" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("unexpected request %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		raw, _ := io.ReadAll(r.Body)
		if captured != nil {
			if err := json.Unmarshal(raw, captured); err != nil {
				t.Errorf("request is not JSON: %v", err)
			}
		}
		n := int(hits.Add(1)) - 1
		status := http.StatusOK
		if len(statuses) > 0 {
			status = statuses[min(n, len(statuses)-1)]
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"try later"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		events.write(w)
	}))
	t.Cleanup(srv.Close)
	return NewAnthropicModel(ProviderConfig{Model: "claude-test", APIKey: "sk-ant", BaseURL: srv.URL + "/"}), &hits
}

func TestAnthropicRequestShape(t *testing.T) {
	var body map[string]any
	m, _ := fakeAnthropic(t, anthropicHello, &body)
	req := ChatRequest{
		Messages: []Message{
			NewSystemMessage("sys"),
			NewUserMessage("hi"),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ls", Args: map[string]any{"path": "/"}}}},
			{Role: RoleTool, ToolCallID: "c1", Name: "ls", Status: ToolStatusError, Content: []ContentBlock{TextBlock("denied")}},
			NewSystemMessage("<repair-state>"),
		},
		Tools:      []ToolSpec{{Name: "ls", Description: "list", Parameters: map[string]any{"type": "object"}}},
		ToolChoice: "required",
		Settings:   map[string]any{"max_tokens": 1000, "temperature": 0.2},
	}
	if _, err := m.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "claude-test" || body["stream"] != true || body["max_tokens"] != float64(1000) || body["temperature"] != 0.2 {
		t.Fatalf("top level = %v", body)
	}
	system := body["system"].([]any)
	if len(system) != 1 || system[0].(map[string]any)["text"] != "sys" || system[0].(map[string]any)["cache_control"] == nil {
		t.Fatalf("system = %v", system)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	use := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if use["type"] != "tool_use" || use["id"] != "c1" || use["input"].(map[string]any)["path"] != "/" {
		t.Fatalf("assistant = %v", msgs[1])
	}
	last := msgs[2].(map[string]any)
	blocks := last["content"].([]any)
	result, tail := blocks[0].(map[string]any), blocks[1].(map[string]any)
	if last["role"] != "user" || result["type"] != "tool_result" || result["tool_use_id"] != "c1" || result["is_error"] != true {
		t.Fatalf("tool result = %v", last)
	}
	if result["cache_control"] == nil || tail["cache_control"] != nil || !strings.Contains(tail["text"].(string), "<repair-state>") {
		t.Fatalf("the cache breakpoint belongs on the tool result, before the changing tail: %v", blocks)
	}
	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "ls" || tool["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("tool = %v", tool)
	}
	if choice := body["tool_choice"].(map[string]any); choice["type"] != "any" {
		t.Fatalf("tool_choice = %v", choice)
	}
}

func TestAnthropicGenerateText(t *testing.T) {
	m, _ := fakeAnthropic(t, anthropicHello, nil)
	got, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleAssistant || got.Text() != "Hello" || got.Metadata["finish_reason"] != "end_turn" {
		t.Fatalf("reply = %+v", got)
	}
	want := Usage{InputTokens: 125, OutputTokens: 7, TotalTokens: 132, CacheRead: 100, CacheWrite: 20}
	if got.Usage == nil || *got.Usage != want {
		t.Fatalf("usage = %+v, want %+v", got.Usage, want)
	}
}

func TestAnthropicStreamYieldsTextDeltas(t *testing.T) {
	m, _ := fakeAnthropic(t, anthropicHello, nil)
	deltas, final := collectStream(t, m)
	if strings.Join(deltas, "|") != "Hel|lo" || final.Text() != "Hello" {
		t.Fatalf("deltas = %q, final = %+v", deltas, final)
	}
}

func TestAnthropicToolCallArgumentsStreamInPieces(t *testing.T) {
	m, _ := fakeAnthropic(t, anthropicEvents{
		{"message_start", `{"type":"message_start","message":{"usage":{"input_tokens":3}}}`},
		{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"look first"}}`},
		{"content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"write_file","input":{}}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":"}}`},
		{"content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"/a.txt\"}"}}`},
		{"content_block_start", `{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"ls","input":{}}}`},
		{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`},
		{"message_stop", `{"type":"message_stop"}`},
	}, nil)
	deltas, final := collectStream(t, m)
	if len(deltas) != 0 {
		t.Fatalf("thinking and tool input must not stream as text: %q", deltas)
	}
	if len(final.ToolCalls) != 2 || final.ToolCalls[0].ID != "toolu_1" || final.ToolCalls[0].Args["file_path"] != "/a.txt" {
		t.Fatalf("tool calls = %+v", final.ToolCalls)
	}
	if second := final.ToolCalls[1]; second.Name != "ls" || second.Args == nil || len(second.Args) != 0 {
		t.Fatalf("a call without input = %+v", second)
	}
	if len(final.Content) != 1 || final.Content[0].Type != BlockReasoning || final.Content[0].Text != "look first" {
		t.Fatalf("content = %+v", final.Content)
	}
}

func TestAnthropicErrorStatus(t *testing.T) {
	m, hits := fakeAnthropic(t, nil, nil, http.StatusUnauthorized)
	_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "try later") || hits.Load() != 1 {
		t.Fatalf("err = %v after %d requests", err, hits.Load())
	}
}

func TestAnthropicRetriesOverload(t *testing.T) {
	fastRetries(t)
	m, hits := fakeAnthropic(t, anthropicHello, nil, 529, http.StatusOK)
	got, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil || got.Text() != "Hello" || hits.Load() != 2 {
		t.Fatalf("reply = %+v, %v after %d requests", got, err, hits.Load())
	}
}

func TestAnthropicContextOverflowIsTyped(t *testing.T) {
	for _, message := range []string{
		"prompt is too long: 210000 tokens > 200000 maximum",
		"input length and `max_tokens` exceed context limit: 190000 + 32000 > 200000",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": message}})
			_, _ = w.Write(body)
		}))
		m := NewAnthropicModel(ProviderConfig{Model: "claude-test", APIKey: "sk-ant", BaseURL: srv.URL})
		_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
		srv.Close()
		if !errors.Is(err, ErrContextOverflow) {
			t.Errorf("%q: want ErrContextOverflow, got %v", message, err)
		}
	}
}

func TestAnthropicErrorEventEndsTheStream(t *testing.T) {
	m, _ := fakeAnthropic(t, anthropicEvents{
		anthropicHello[0], anthropicHello[1], anthropicHello[3],
		{"error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
	}, nil)
	_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestAnthropicStreamWithoutMessageStopIsAnError(t *testing.T) {
	m, _ := fakeAnthropic(t, anthropicHello[:4], nil)
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}); err == nil {
		t.Fatal("a truncated stream was accepted")
	}
}

func TestAnthropicProviderRequiresAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	_, err := InitChatModel("anthropic:claude-test", nil)
	if err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("want missing key error, got %v", err)
	}
}
