package toolexclusion

import (
	"context"
	"slices"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func TestExcludedToolsAreNotSentToTheModel(t *testing.T) {
	tools := []tool.Tool{&tool.Func{ToolName: "ls"}, &tool.Func{ToolName: "execute"}, &tool.Func{ToolName: "grep"}}
	req := &agent.ModelRequest{Tools: tools}
	var sent []string
	_, err := New([]string{"execute"}).WrapModelCall(context.Background(), req, func(_ context.Context, r *agent.ModelRequest) (*agent.ModelResponse, error) {
		for _, tl := range r.Tools {
			sent = append(sent, tl.Name())
		}
		return &agent.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sent, []string{"ls", "grep"}) {
		t.Fatalf("sent tools = %v", sent)
	}
	if len(tools) != 3 || tools[1].Name() != "execute" {
		t.Fatal("the caller's tool slice must not be modified")
	}
}
