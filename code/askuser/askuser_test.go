package askuser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/hitl"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func questionsArg() map[string]any {
	return map[string]any{"questions": []any{map[string]any{
		"id": "db", "prompt": "Which database?",
		"options": []any{
			map[string]any{"id": "pg", "label": "Postgres (Recommended)"},
			map[string]any{"id": "sqlite", "label": "SQLite"},
		},
	}}}
}

func TestParseQuestionsRejectsMalformed(t *testing.T) {
	cases := map[string]map[string]any{
		"empty":       {"questions": []any{}},
		"one option":  {"questions": []any{map[string]any{"id": "q", "prompt": "?", "options": []any{map[string]any{"id": "a", "label": "A"}}}}},
		"no prompt":   {"questions": []any{map[string]any{"id": "q", "options": []any{map[string]any{"id": "a", "label": "A"}, map[string]any{"id": "b", "label": "B"}}}}},
		"dup options": {"questions": []any{map[string]any{"id": "q", "prompt": "?", "options": []any{map[string]any{"id": "a", "label": "A"}, map[string]any{"id": "a", "label": "B"}}}}},
	}
	for name, args := range cases {
		if _, err := ParseQuestions(args); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := ParseQuestions(questionsArg()); err != nil {
		t.Fatal(err)
	}
}

func TestFormatAnswers(t *testing.T) {
	qs, _ := ParseQuestions(questionsArg())
	got := FormatAnswers(qs, []Answer{{QuestionID: "db", OptionIDs: []string{"sqlite"}, Other: "in-memory for tests"}})
	if !strings.Contains(got, "Which database?") || !strings.Contains(got, "SQLite; Other: in-memory for tests") {
		t.Fatalf("got %q", got)
	}
}

func newAgent(t *testing.T, script ...llm.Message) agent.Agent {
	t.Helper()
	model := llm.NewFakeModel(append(script, llm.NewAssistantMessage("done"))...)
	a, err := agent.CreateAgent(agent.Config{
		Model:        model,
		Tools:        []tool.Tool{New()},
		Middleware:   []agent.Middleware{hitl.New(map[string]hitl.InterruptOnConfig{ToolName: InterruptOn()})},
		Checkpointer: runtime.NewMemoryCheckpointer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAskUserPausesUnderAutoApproveAndRespondBecomesResult(t *testing.T) {
	a := newAgent(t, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "q1", Name: ToolName, Args: questionsArg()}}})
	cfg := runtime.RunConfig{ThreadID: "t", Configurable: map[string]any{hitl.AutoApproveKey: true}}
	_, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, cfg)
	var paused *runtime.InterruptError
	if !errors.As(err, &paused) {
		t.Fatalf("err = %v, want interrupt", err)
	}
	resume := &runtime.Command{Resume: map[string]any{"after_model": []hitl.Decision{{Type: hitl.DecisionRespond, Message: "The user answered: SQLite"}}}}
	st, err := a.Invoke(context.Background(), agent.Input{Resume: resume}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Messages[2]; got.ToolCallID != "q1" || got.Text() != "The user answered: SQLite" {
		t.Fatalf("tool message = %+v", got)
	}
}

func TestMalformedAskUserReachesModelAsError(t *testing.T) {
	a := newAgent(t, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "q1", Name: ToolName, Args: map[string]any{"questions": []any{}}}}})
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{ThreadID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Messages[2]; got.Status != llm.ToolStatusError || !strings.Contains(got.Text(), "non-empty") {
		t.Fatalf("tool message = %+v", got)
	}
}
