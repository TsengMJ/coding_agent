package client

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/code/askuser"
	"github.com/tsengmingchun/quill/middleware/hitl"
)

func TestPromptApproverAnswersAskUserAndApprovesTheRest(t *testing.T) {
	ask := hitl.ActionRequest{ToolCallID: "q", Name: askuser.ToolName, Args: map[string]any{"questions": []any{map[string]any{
		"id": "db", "prompt": "Which DB?", "options": []any{
			map[string]any{"id": "pg", "label": "Postgres"}, map[string]any{"id": "lite", "label": "SQLite"},
		},
	}}}}
	write := hitl.ActionRequest{ToolCallID: "w", Name: "write_file", Args: map[string]any{"file_path": "/a", "content": "x"}}
	p := PromptApprover{In: bufio.NewReader(strings.NewReader("a\n2\n")), Out: &bytes.Buffer{}}
	got, err := p.Decide(context.Background(), []hitl.ActionRequest{write, ask, write})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Type != hitl.DecisionApprove || got[2].Type != hitl.DecisionApprove {
		t.Fatalf("decisions = %+v", got)
	}
	if got[1].Type != hitl.DecisionRespond || !strings.Contains(got[1].Message, "-> SQLite") {
		t.Fatalf("ask decision = %+v", got[1])
	}
}
