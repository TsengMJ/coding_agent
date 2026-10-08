package llm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var (
	_ ChatModel = (*AnthropicModel)(nil)
	_ ChatModel = (*OpenAIModel)(nil)
	_ ChatModel = (*FakeModel)(nil)
)

func TestNewToolMessageLinksCall(t *testing.T) {
	msg := NewToolMessage("call-1", "ls", "ok")
	if msg.Role != RoleTool || msg.ToolCallID != "call-1" {
		t.Fatalf("unexpected tool message: %+v", msg)
	}
}

func TestMessageTextJoinsTextBlocks(t *testing.T) {
	m := Message{Content: []ContentBlock{TextBlock("a"), {Type: BlockImage, URL: "x"}, TextBlock("b")}}
	if got := m.Text(); got != "ab" {
		t.Fatalf("Text() = %q, want %q", got, "ab")
	}
}

func TestParseModelSpec(t *testing.T) {
	got, err := ParseModelSpec("openai:gpt-5.5")
	if err != nil || got != (ModelSpec{Provider: "openai", Model: "gpt-5.5"}) {
		t.Fatalf("ParseModelSpec = %+v, %v", got, err)
	}
}

func TestParseModelSpecKeepsColonsInModel(t *testing.T) {
	got, err := ParseModelSpec("ollama:llama3:8b")
	if err != nil || got.Model != "llama3:8b" {
		t.Fatalf("ParseModelSpec = %+v, %v", got, err)
	}
}

func TestParseModelSpecRejectsMissingProvider(t *testing.T) {
	for _, spec := range []string{"gpt-5.5", ":gpt-5.5", "openai:"} {
		if _, err := ParseModelSpec(spec); err == nil {
			t.Errorf("ParseModelSpec(%q) want error", spec)
		}
	}
}

func TestInitChatModelUsesRegisteredFactory(t *testing.T) {
	var gotModel string
	err := RegisterProvider("test-provider", func(model string, _ map[string]any) (ChatModel, error) {
		gotModel = model
		return NewFakeModel(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitChatModel("test-provider:m1", nil); err != nil {
		t.Fatal(err)
	}
	if gotModel != "m1" {
		t.Fatalf("factory got model %q, want m1", gotModel)
	}
}

func TestInitChatModelUnknownProviderListsRegistered(t *testing.T) {
	_, err := InitChatModel("nope:x", nil)
	if err == nil || !strings.Contains(err.Error(), "registered:") {
		t.Fatalf("want unknown-provider error listing providers, got %v", err)
	}
}

func TestFakeModelReplaysScriptThenErrors(t *testing.T) {
	f := NewFakeModel(NewAssistantMessage("one"))
	ctx := context.Background()
	got, err := f.Generate(ctx, ChatRequest{Messages: []Message{NewUserMessage("hi")}})
	if err != nil || got.Text() != "one" {
		t.Fatalf("first Generate = %+v, %v", got, err)
	}
	if _, err := f.Generate(ctx, ChatRequest{}); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("want ErrScriptExhausted, got %v", err)
	}
	if n := len(f.Requests()); n != 2 {
		t.Fatalf("recorded %d requests, want 2", n)
	}
}
