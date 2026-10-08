package unsupportedcontent

import (
	"context"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

var png = llm.ContentBlock{Type: llm.BlockImage, MimeType: "image/png", Base64: "iVBORw0K"}

func history() []llm.Message {
	return []llm.Message{
		llm.NewUserMessage("check the page"),
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "browser_take_screenshot"}, {ID: "c2", Name: "browser_snapshot"}}},
		{Role: llm.RoleTool, Name: "browser_take_screenshot", ToolCallID: "c1", Content: []llm.ContentBlock{llm.TextBlock("Took a screenshot"), png}},
		{Role: llm.RoleTool, Name: "browser_snapshot", ToolCallID: "c2", Content: []llm.ContentBlock{llm.TextBlock("- button")}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock("done")}},
	}
}

func sent(t *testing.T, profile llm.ModelProfile, msgs []llm.Message) []llm.Message {
	t.Helper()
	req := &agent.ModelRequest{Model: llm.NewFakeModel().WithProfile(profile), Messages: msgs}
	var got []llm.Message
	_, err := New().WrapModelCall(context.Background(), req, func(_ context.Context, r *agent.ModelRequest) (*agent.ModelResponse, error) {
		got = r.Messages
		return &agent.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func hasImage(m llm.Message) bool {
	for _, c := range m.Content {
		if c.Type == llm.BlockImage {
			return true
		}
	}
	return false
}

func TestToolImagesMoveToAUserMessageAfterTheToolResults(t *testing.T) {
	got := sent(t, llm.ModelProfile{}, history())
	if len(got) != 6 {
		t.Fatalf("got %d messages, want 6", len(got))
	}
	if hasImage(got[2]) || got[2].Role != llm.RoleTool || got[2].ToolCallID != "c1" {
		t.Errorf("tool message still has the image: %+v", got[2])
	}
	if got[3].Role != llm.RoleTool {
		t.Errorf("tool results must stay together, got %s at index 3", got[3].Role)
	}
	moved := got[4]
	if moved.Role != llm.RoleUser || !hasImage(moved) {
		t.Fatalf("message 4 = %+v, want a user message with the image", moved)
	}
	if last := moved.Content[len(moved.Content)-1]; last.Base64 != png.Base64 || last.MimeType != png.MimeType {
		t.Errorf("image changed: %+v", moved.Content)
	}
}

func TestToolImagesStayWhenTheModelAcceptsThem(t *testing.T) {
	got := sent(t, llm.ModelProfile{ToolMessageImageInputs: true}, history())
	if len(got) != 5 || !hasImage(got[2]) {
		t.Fatalf("messages changed: %+v", got)
	}
}

func TestHistoryWithoutToolImagesIsUnchanged(t *testing.T) {
	msgs := history()[:2]
	got := sent(t, llm.ModelProfile{}, msgs)
	if len(got) != 2 {
		t.Fatalf("messages changed: %+v", got)
	}
}

func TestTheCallersMessagesAreNotModified(t *testing.T) {
	msgs := history()
	sent(t, llm.ModelProfile{}, msgs)
	if !hasImage(msgs[2]) || len(msgs) != 5 {
		t.Fatal("the request history must not be modified in place")
	}
}
