package subagents

import (
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
)

func texts(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role) + ":" + m.Text()
	}
	return out
}

func forkMiddleware(t *testing.T, sub *llm.FakeModel) *Middleware {
	t.Helper()
	mw, err := New(Options{SubAgents: []Spec{
		SubAgent{Name: "fork", Description: "Continues the conversation", Mode: ModeFork, Model: sub},
		SubAgent{Name: "iso", Description: "Isolated", Model: llm.NewFakeModel(llm.NewAssistantMessage("x"))},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return mw
}

func TestForkReceivesParentConversationThenTask(t *testing.T) {
	sub := llm.NewFakeModel(llm.NewAssistantMessage("forked report"))
	mw := forkMiddleware(t, sub)
	in := agent.Input{Messages: []llm.Message{
		llm.NewUserMessage("the bug is in parse.go line 42"),
		llm.NewAssistantMessage("noted"),
		llm.NewUserMessage("fix it with a subagent"),
	}}
	st := runParent(t, mw, in, taskCall("c1", "fork", "write the fix"))
	if got := toolText(st, "c1"); got != "forked report" {
		t.Fatalf("task result = %q", got)
	}
	got := texts(sub.Requests()[0].Messages)
	want := []string{"user:the bug is in parse.go line 42", "assistant:noted", "user:fix it with a subagent", "user:" + forkTaskPreamble + "write the fix"}
	if !slices.Equal(got, want) {
		t.Fatalf("fork saw %q", got)
	}
}

func TestForkIsMarkedInTaskListing(t *testing.T) {
	mw := forkMiddleware(t, llm.NewFakeModel())
	desc := mw.Tools()[0].Description()
	if !strings.Contains(desc, "- fork: Continues the conversation"+forkedToolNote) || strings.Contains(desc, "- iso: Isolated"+forkedToolNote) {
		t.Fatalf("task description:\n%s", desc)
	}
}

func TestForkCannotDelegateAgain(t *testing.T) {
	sub := llm.NewFakeModel(
		callsMessage(taskCall("inner", "iso", "go deeper")),
		llm.NewAssistantMessage("did it myself"),
	)
	mw := forkMiddleware(t, sub)
	st := runParent(t, mw, agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, taskCall("c1", "fork", "do it"))
	if got := toolText(st, "c1"); got != "did it myself" {
		t.Fatalf("task result = %q", got)
	}
	reqs := sub.Requests()
	var names []string
	for _, s := range reqs[0].Tools {
		names = append(names, s.Name)
	}
	if !slices.Contains(names, TaskToolName) {
		t.Fatalf("a fork keeps the task tool so its tools match the parent's: %v", names)
	}
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if last.Role != llm.RoleTool || last.Text() != forkRecursionRefusal {
		t.Fatalf("inner task result = %+v", last)
	}
	if _, leaked := st.Values[ForkedContextKey]; leaked {
		t.Fatal("the forked-context marker leaked into the parent")
	}
}

func TestForkSeesSummarizedHistory(t *testing.T) {
	sub := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	mw := forkMiddleware(t, sub)
	in := agent.Input{
		Messages: []llm.Message{llm.NewUserMessage("old turn"), llm.NewAssistantMessage("old answer"), llm.NewUserMessage("recent turn")},
		Values: map[string]any{summarization.EventStateKey: summarization.Event{
			CutoffIndex: 2, SummaryMessage: llm.NewUserMessage("SUMMARY of old turns"),
		}},
	}
	runParent(t, mw, in, taskCall("c1", "fork", "continue"))
	got := texts(sub.Requests()[0].Messages)
	want := []string{"user:SUMMARY of old turns", "user:recent turn", "user:" + forkTaskPreamble + "continue"}
	if !slices.Equal(got, want) {
		t.Fatalf("fork saw %q", got)
	}
}
