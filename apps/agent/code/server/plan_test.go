package server

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/askuser"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	"github.com/tsengmingchun/quill/apps/agent/middleware/todolist"
)

func newPlanService(t *testing.T, replies ...llm.Message) (*Service, *llm.FakeModel, string) {
	t.Helper()
	userDir := t.TempDir()
	model := llm.NewFakeModel(replies...)
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: model, NoVerifyGate: true, Paths: config.Paths{UserDir: userDir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, model, userDir
}

func specNames(req llm.ChatRequest) []string {
	var names []string
	for _, s := range req.Tools {
		names = append(names, s.Name)
	}
	return names
}

func TestValidateRejectsBadModes(t *testing.T) {
	cwd := t.TempDir()
	cases := map[string]RunRequest{
		"headless plan":      {Mode: ModePlan},
		"unknown mode":       {Mode: "yolo", Interactive: true},
		"build in plan mode": {Mode: ModePlan, Interactive: true, BuildPlan: true},
	}
	for name, req := range cases {
		req.ThreadID, req.Cwd, req.Messages = "t", cwd, []llm.Message{llm.NewUserMessage("x")}
		if err := validate(&req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: err = %v, want ErrBadRequest", name, err)
		}
	}
	resume := RunRequest{ThreadID: "t", Cwd: cwd, Resume: &Resume{}, BuildPlan: true}
	if err := validate(&resume); !errors.Is(err, ErrBadRequest) {
		t.Errorf("build on resume: err = %v", err)
	}
}

func TestPlanRunThenBuild(t *testing.T) {
	cwd := t.TempDir()
	plan := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "p", Name: planmode.ToolName, Args: map[string]any{
		"name": "Add flag", "overview": "o", "plan": "## Approach\n\nEdit main.go",
		"todos": []any{map[string]any{"id": "flag", "content": "Add the flag in main.go"}},
	}}}}
	svc, model, userDir := newPlanService(t, plan, llm.NewAssistantMessage("here is the plan"), llm.NewAssistantMessage("built"))

	req := userReq("t1", cwd, "plan a flag")
	req.Interactive, req.Mode = true, ModePlan
	if _, err := collect(t, svc, req); err != nil {
		t.Fatal(err)
	}
	if names := specNames(model.Requests()[0]); slices.Contains(names, "write_file") || !slices.Contains(names, askuser.ToolName) {
		t.Fatalf("plan mode tools = %v", names)
	}
	ts, err := svc.GetThread(context.Background(), "t1")
	if err != nil || ts.Plan == nil || ts.Plan.Status != planmode.StatusDraft {
		t.Fatalf("thread plan = %+v, %v", ts, err)
	}
	if saved, err := svc.store.Plans.Get(context.Background(), ts.Plan.ID); err != nil || saved == nil || saved.Name != "Add flag" {
		t.Fatalf("stored plan = %+v, %v", saved, err)
	}
	if entries, _ := os.ReadDir(userDir); len(entries) != 0 {
		t.Fatalf("plan mode wrote files to the user directory: %v", entries)
	}

	build := userReq("t1", cwd, "build it")
	build.Interactive, build.BuildPlan = true, true
	if _, err := collect(t, svc, build); err != nil {
		t.Fatal(err)
	}
	ts, _ = svc.GetThread(context.Background(), "t1")
	if ts.Plan.Status != planmode.StatusBuilding || len(ts.Todos) != 1 || ts.Todos[0].Status != todolist.StatusInProgress {
		t.Fatalf("after build: plan %+v todos %+v", ts.Plan, ts.Todos)
	}
	if names := specNames(model.Requests()[2]); !slices.Contains(names, "write_file") || slices.Contains(names, planmode.ToolName) {
		t.Fatalf("build tools = %v", names)
	}
}

func TestAskUserInterruptsEvenWithAutoApprove(t *testing.T) {
	cwd := t.TempDir()
	ask := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "q", Name: askuser.ToolName, Args: map[string]any{
		"questions": []any{map[string]any{"id": "db", "prompt": "Which DB?", "options": []any{
			map[string]any{"id": "pg", "label": "Postgres"}, map[string]any{"id": "lite", "label": "SQLite"},
		}}},
	}}}}
	svc, _, _ := newPlanService(t, ask, llm.NewAssistantMessage("thanks"))
	req := userReq("t1", cwd, "plan")
	req.Interactive, req.Mode = true, ModePlan
	events, err := collect(t, svc, req)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != EventInterrupt || last.Interrupts[0].ActionRequests[0].Name != askuser.ToolName {
		t.Fatalf("last event = %+v", last)
	}
	resume := resumeReq("t1", cwd, hitl.Decision{Type: hitl.DecisionRespond, Message: "The user answered: SQLite"})
	resume.Interactive, resume.Mode, resume.AutoApprove = true, ModePlan, true
	events, err = collect(t, svc, resume)
	if err != nil {
		t.Fatal(err)
	}
	var answer string
	for _, m := range messages(events) {
		if m.ToolCallID == "q" {
			answer = m.Text()
		}
	}
	if answer != "The user answered: SQLite" {
		t.Fatalf("answer = %q", answer)
	}
}
