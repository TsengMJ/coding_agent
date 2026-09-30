package deepagents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/permissions"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

var writeOnly = []permissions.Operation{permissions.OpWrite}

func TestPermissionsDenyInMainAgent(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/locked/a.txt", "content": "x"}}),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model), WithPermissions(permissions.FilesystemPermission{
		Operations: writeOnly, Paths: []string{"/locked/**"}, Mode: permissions.ModeDeny,
	}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(st, "w"); got != "Error: permission denied for write on /locked/a.txt" {
		t.Fatalf("write_file result = %q", got)
	}
}

func TestPermissionsInterruptRulePausesForReview(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(
			llm.ToolCall{ID: "free", Name: "write_file", Args: map[string]any{"file_path": "/notes.txt", "content": "x"}},
			llm.ToolCall{ID: "gated", Name: "write_file", Args: map[string]any{"file_path": "/config/app.yaml", "content": "y"}},
		),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model), WithCheckpointer(runtime.NewMemoryCheckpointer()),
		WithPermissions(permissions.FilesystemPermission{Operations: writeOnly, Paths: []string{"/config/**"}, Mode: permissions.ModeInterrupt}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := runtime.RunConfig{ThreadID: "t"}
	_, err = a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, cfg)
	var paused *runtime.InterruptError
	if !errors.As(err, &paused) {
		t.Fatalf("want interrupt, got %v", err)
	}
	reqs, ok := paused.Interrupts[0].Value.([]hitl.ActionRequest)
	if !ok || len(reqs) != 1 || reqs[0].ToolCallID != "gated" {
		t.Fatalf("action requests = %#v", paused.Interrupts[0].Value)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Resume: &runtime.Command{Resume: map[string]any{
		"after_model": []hitl.Decision{{Type: hitl.DecisionReject}},
	}}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	files := stateFiles(t, st)
	if _, ok := files["/config/app.yaml"]; ok {
		t.Fatal("rejected write ran")
	}
	if _, ok := files["/notes.txt"]; !ok {
		t.Fatalf("ungated write missing: %v", files)
	}
}

func TestUserInterruptOnOverridesGeneratedConfig(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/config/a", "content": "x"}}),
		llm.NewAssistantMessage("done"),
	)
	never := func(llm.ToolCall) bool { return false }
	a, err := CreateDeepAgent(WithModel(model),
		WithPermissions(permissions.FilesystemPermission{Operations: writeOnly, Paths: []string{"/config/**"}, Mode: permissions.ModeInterrupt}),
		WithInterruptOn(map[string]hitl.InterruptOnConfig{"write_file": {When: never}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{}); err != nil {
		t.Fatalf("user config must win, got %v", err)
	}
}

func TestSubagentsInheritPermissionsWithInterruptAsDeny(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "t", Name: "task", Args: map[string]any{"description": "edit config", "subagent_type": "general-purpose"}}),
		toolCalls(llm.ToolCall{ID: "sw", Name: "write_file", Args: map[string]any{"file_path": "/config/app.yaml", "content": "x"}}),
		llm.NewAssistantMessage("could not write"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model),
		WithPermissions(permissions.FilesystemPermission{Operations: writeOnly, Paths: []string{"/config/**"}, Mode: permissions.ModeInterrupt}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if files, _ := st.Values[backend.FilesStateKey].(map[string]backend.FileData); files["/config/app.yaml"].Content != "" {
		t.Fatal("subagent bypassed the interrupt rule")
	}
	sub := model.Requests()[2].Messages
	if got := sub[len(sub)-1].Text(); got != "Error: permission denied for write on /config/app.yaml" {
		t.Fatalf("subagent tool result = %q", got)
	}
}

func TestDeclarativeSubagentOwnPermissionsReplaceParents(t *testing.T) {
	model := llm.NewFakeModel(
		toolCalls(llm.ToolCall{ID: "t", Name: "task", Args: map[string]any{"description": "write", "subagent_type": "writer"}}),
		toolCalls(llm.ToolCall{ID: "sw", Name: "write_file", Args: map[string]any{"file_path": "/locked/a.txt", "content": "x"}}),
		llm.NewAssistantMessage("wrote"),
		llm.NewAssistantMessage("done"),
	)
	a, err := CreateDeepAgent(WithModel(model),
		WithPermissions(permissions.FilesystemPermission{Operations: writeOnly, Paths: []string{"/locked/**"}, Mode: permissions.ModeDeny}),
		WithSubAgents(subagents.SubAgent{
			Name: "writer", Description: "writes", SystemPrompt: "write",
			Permissions: []permissions.FilesystemPermission{{Operations: writeOnly, Paths: []string{"/other/**"}, Mode: permissions.ModeDeny}},
		}))
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), agent.Input{Messages: []llm.Message{llm.NewUserMessage("go")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := stateFiles(t, st)["/locked/a.txt"].Content; got != "x" {
		t.Fatalf("subagent's own rules should allow /locked, file = %q", got)
	}
}

func TestPermissionsRejectedWithShellBackend(t *testing.T) {
	b := backend.NewLocalShellBackend(backend.LocalShellOptions{FilesystemOptions: backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true}})
	_, err := CreateDeepAgent(WithModel(llm.NewFakeModel()), WithBackend(b),
		WithPermissions(permissions.FilesystemPermission{Operations: writeOnly, Paths: []string{"/x"}, Mode: permissions.ModeDeny}))
	if err == nil || !strings.Contains(err.Error(), "command execution") {
		t.Fatalf("err = %v", err)
	}
}
