package llm

import (
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

var (
	_ ChatModel = (*AnthropicModel)(nil)
	_ ChatModel = (*OpenAIModel)(nil)
)

func TestInitChatModelNotImplemented(t *testing.T) {
	if _, err := InitChatModel("openai:gpt-5.5", nil); !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestNewToolMessageLinksCall(t *testing.T) {
	msg := NewToolMessage("call-1", "ls", "ok")
	if msg.Role != RoleTool || msg.ToolCallID != "call-1" {
		t.Fatalf("unexpected tool message: %+v", msg)
	}
}
