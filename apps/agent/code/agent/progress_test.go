package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

// scripted is one tool call and its result in a test history.
type scripted struct {
	name   string
	args   map[string]any
	result string
	failed bool
}

func history(steps ...scripted) []llm.Message {
	msgs := []llm.Message{llm.NewUserMessage("fix the bug")}
	for i, s := range steps {
		id := "s" + string(rune('a'+i))
		if s.name == "" {
			msgs = append(msgs, llm.NewUserMessage(s.result))
			continue
		}
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: s.name, Args: s.args}}})
		res := llm.NewToolMessage(id, s.name, s.result)
		if s.failed {
			res.Status = llm.ToolStatusError
		}
		msgs = append(msgs, res)
	}
	return msgs
}

var (
	patchArgs = map[string]any{"patch": "*** Begin Patch\n*** Update File: /a.go\n@@\n-x\n+y\n*** End Patch"}
	badPatch  = scripted{name: "apply_patch", args: patchArgs, result: "Error: context not found in /a.go", failed: true}
	testCmd   = map[string]any{"command": "go test ./..."}
)

func failingTest(seconds string) scripted {
	return scripted{name: "execute", args: testCmd, result: "--- FAIL: TestA (" + seconds + "s)\nFAIL\tm\t" + seconds + "s\n[Command failed with exit code 1]"}
}

func TestRepeatRefusal(t *testing.T) {
	edit := scripted{name: "edit_file", args: map[string]any{"file_path": "/a.go"}, result: "Successfully replaced 1 instance"}
	tests := []struct {
		name    string
		history []llm.Message
		call    map[string]any
		refuse  bool
	}{
		{"one failure", history(badPatch), patchArgs, false},
		{"two identical failures", history(badPatch, badPatch), patchArgs, true},
		{"timings are ignored", history(failingTest("0.12"), failingTest("0.31")), testCmd, true},
		{"different failures", history(failingTest("1"), scripted{name: "execute", args: testCmd, result: "panic: nil map\n[Command failed with exit code 2]"}), testCmd, false},
		{"edit in between", history(failingTest("1"), edit, failingTest("1")), testCmd, false},
		{"shell command in between", history(failingTest("1"), scripted{name: "execute", args: map[string]any{"command": "npm install"}, result: "[Command succeeded with exit code 0]"}, failingTest("1")), testCmd, false},
		{"user turn in between", history(failingTest("1"), scripted{result: "try again"}, failingTest("1")), testCmd, false},
		{"other call", history(badPatch, badPatch), testCmd, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name := "apply_patch"
			if tc.call["command"] != nil {
				name = "execute"
			}
			got := RepeatRefusal(tc.history, llm.ToolCall{ID: "next", Name: name, Args: tc.call})
			if (got != "") != tc.refuse {
				t.Fatalf("refusal = %q, want refuse=%v", got, tc.refuse)
			}
		})
	}
}

func TestProgressNoteOnSecondIdenticalFailure(t *testing.T) {
	call := llm.ToolCall{ID: "next", Name: "apply_patch", Args: patchArgs}
	if note := ProgressNote(history(), call, badPatch.result, llm.ToolStatusError); note != "" {
		t.Fatalf("first failure got a note: %q", note)
	}
	if note := ProgressNote(history(badPatch), call, badPatch.result, llm.ToolStatusError); !strings.Contains(note, "same failure") {
		t.Fatalf("second failure note = %q", note)
	}
	if note := ProgressNote(history(badPatch), call, "Applied", llm.ToolStatusSuccess); note != "" {
		t.Fatalf("success got a note: %q", note)
	}
}

func TestAgentRefusesThirdIdenticalFailingPatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package a\n")
	patch := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "apply_patch", Args: patchArgs}}}
	}
	model := llm.NewFakeModel(patch("p1"), patch("p2"), patch("p3"), llm.NewAssistantMessage("Giving up on the patch."))
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("change x to y")}}, rt.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	var results []string
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool {
			results = append(results, m.Text())
		}
	}
	if len(results) != 3 {
		t.Fatalf("got %d tool results", len(results))
	}
	if strings.Contains(results[0], progressMarker) || !strings.Contains(results[1], "same failure") || !strings.HasPrefix(results[2], progressMarker+" Not run") {
		t.Fatalf("results = %q", results)
	}
}
