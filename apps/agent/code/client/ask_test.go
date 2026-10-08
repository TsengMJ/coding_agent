package client

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/askuser"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
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

func TestParseAnswerLine(t *testing.T) {
	q := askuser.Question{ID: "q", Options: []askuser.Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}}}
	if a := ParseAnswerLine(q, "2"); len(a.OptionIDs) != 1 || a.OptionIDs[0] != "b" {
		t.Fatalf("single = %+v", a)
	}
	if a := ParseAnswerLine(q, "1,3"); len(a.OptionIDs) != 1 {
		t.Fatalf("single-choice question took several: %+v", a)
	}
	q.AllowMultiple = true
	if a := ParseAnswerLine(q, "1, 3"); strings.Join(a.OptionIDs, ",") != "a,c" {
		t.Fatalf("multi = %+v", a)
	}
	if a := ParseAnswerLine(q, "use redis"); a.Other != "use redis" || a.OptionIDs != nil {
		t.Fatalf("other = %+v", a)
	}
	if a := ParseAnswerLine(q, "9"); a.Other != "9" {
		t.Fatalf("out of range = %+v", a)
	}
}
