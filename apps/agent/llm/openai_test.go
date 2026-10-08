package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOpenAI serves one canned response and captures the request body.
func fakeOpenAI(t *testing.T, status int, reply string, captured *map[string]any) *OpenAIModel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected request %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if captured != nil {
			raw, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(raw, captured); err != nil {
				t.Errorf("request is not JSON: %v", err)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIModel(ProviderConfig{Model: "gpt-test", APIKey: "sk-test", BaseURL: srv.URL + "/"}, false)
}

const textReply = `{"id":"r1","model":"gpt-test","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],
"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`

func TestOpenAIRequestShape(t *testing.T) {
	var body map[string]any
	m := fakeOpenAI(t, 200, textReply, &body)
	req := ChatRequest{
		Messages: []Message{
			NewSystemMessage("sys"),
			NewUserMessage("hi"),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ls", Args: map[string]any{"path": "/"}}}},
			NewToolMessage("c1", "ls", "a.txt"),
		},
		Tools: []ToolSpec{{Name: "ls", Description: "list", Parameters: map[string]any{"type": "object"}}},
	}
	if _, err := m.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-test" {
		t.Fatalf("model = %v", body["model"])
	}
	msgs := body["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if fn["name"] != "ls" || fn["arguments"] != `{"path":"/"}` {
		t.Fatalf("unexpected assistant message: %v", assistant)
	}
	if content, ok := assistant["content"].(string); ok && content != "" {
		t.Fatalf("assistant content = %q", content)
	}
	if toolMsg := msgs[3].(map[string]any); toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "c1" {
		t.Fatalf("unexpected tool message: %v", toolMsg)
	}
	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["name"] != "ls" {
		t.Fatalf("unexpected tool spec: %v", tool)
	}
}

func TestOpenAIKeepsTrailingSystemMessageInPlace(t *testing.T) {
	var body map[string]any
	m := fakeOpenAI(t, 200, textReply, &body)
	req := ChatRequest{Messages: []Message{
		NewSystemMessage("sys"),
		NewUserMessage("hi"),
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "ls"}}},
		NewToolMessage("c1", "ls", "a.txt"),
		NewSystemMessage("<repair-state>\nphase: patching\n</repair-state>"),
	}}
	if _, err := m.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if len(msgs) != 5 || last["role"] != "system" || !strings.Contains(last["content"].(string), "<repair-state>") {
		t.Fatalf("messages = %v", msgs)
	}
}

func TestOpenAITextReply(t *testing.T) {
	m := fakeOpenAI(t, 200, textReply, nil)
	got, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != RoleAssistant || got.Text() != "hello" || got.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected reply: %+v", got)
	}
}

func TestOpenAIToolCallReply(t *testing.T) {
	reply := `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[
{"id":"call_1","type":"function","function":{"name":"ls","arguments":"{\"path\":\"/tmp\"}"}}]},"finish_reason":"tool_calls"}]}`
	m := fakeOpenAI(t, 200, reply, nil)
	got, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != "call_1" || got.ToolCalls[0].Args["path"] != "/tmp" {
		t.Fatalf("unexpected tool calls: %+v", got.ToolCalls)
	}
}

func TestOpenAIErrorStatus(t *testing.T) {
	m := fakeOpenAI(t, 401, `{"error":{"message":"bad key"}}`, nil)
	_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("want 401 error with body, got %v", err)
	}
}

func TestOpenAIProviderRequiresAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := InitChatModel("openai:gpt-test", nil)
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("want missing key error, got %v", err)
	}
}

func TestOpenAIContextOverflowIsTyped(t *testing.T) {
	m := fakeOpenAI(t, 400, `{"error":{"message":"This model's maximum context length is 128000 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`, nil)
	_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("want ErrContextOverflow, got %v", err)
	}
}

func TestOpenAIOtherBadRequestIsNotOverflow(t *testing.T) {
	m := fakeOpenAI(t, 400, `{"error":{"message":"invalid tool schema","type":"invalid_request_error"}}`, nil)
	_, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err == nil || errors.Is(err, ErrContextOverflow) {
		t.Fatalf("got %v", err)
	}
}

func TestOpenAISendsUserImagesAsDataURLs(t *testing.T) {
	var body map[string]any
	m := fakeOpenAI(t, 200, textReply, &body)
	user := Message{Role: RoleUser, Content: []ContentBlock{
		TextBlock("what is this?"),
		{Type: BlockImage, MimeType: "image/png", Base64: "iVBORw0KGgo="},
		{Type: BlockImage, URL: "https://example.com/a.jpg"},
	}}
	if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{user}}); err != nil {
		t.Fatal(err)
	}
	parts := body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(parts) != 3 || parts[0].(map[string]any)["text"] != "what is this?" {
		t.Fatalf("content = %v", parts)
	}
	for i, want := range []string{"data:image/png;base64,iVBORw0KGgo=", "https://example.com/a.jpg"} {
		p := parts[i+1].(map[string]any)
		if p["type"] != "image_url" || p["image_url"].(map[string]any)["url"] != want {
			t.Errorf("part %d = %v", i+1, p)
		}
	}
}

func TestOpenAIRejectsImagesOutsideUserMessages(t *testing.T) {
	m := fakeOpenAI(t, 200, textReply, nil)
	img := ContentBlock{Type: BlockImage, MimeType: "image/png", Base64: "x"}
	for _, msg := range []Message{
		{Role: RoleTool, ToolCallID: "c1", Content: []ContentBlock{img}},
		{Role: RoleUser, Content: []ContentBlock{{Type: BlockAudio, MimeType: "audio/wav", Base64: "x"}}},
	} {
		if _, err := m.Generate(context.Background(), ChatRequest{Messages: []Message{msg}}); err == nil {
			t.Errorf("%s message with %s content was accepted", msg.Role, msg.Content[0].Type)
		}
	}
}
