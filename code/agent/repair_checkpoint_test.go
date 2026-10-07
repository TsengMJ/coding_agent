package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

func workspace(t *testing.T, dir string) backend.SandboxBackend {
	t.Helper()
	sb, ok := NewBackend(dir).(backend.SandboxBackend)
	if !ok {
		t.Skip("backend cannot run commands")
	}
	return sb
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestSaveCheckpointLeavesHeadAndIndexAlone(t *testing.T) {
	dir := goRepo(t, nil)
	head := gitOut(t, dir, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(dir, "m.go"), mathGo+"\nfunc Mul(a, b int) int { return a * b }\n")
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n")
	status := gitOut(t, dir, "status", "--porcelain")
	commit, err := saveCheckpoint(context.Background(), workspace(t, dir), checkpointRef("t1", 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, dir, "rev-parse", "refs/quill/checkpoints/t1/1"); got != commit {
		t.Fatalf("ref = %q, commit = %q", got, commit)
	}
	if gitOut(t, dir, "rev-parse", "HEAD") != head || gitOut(t, dir, "status", "--porcelain") != status {
		t.Fatalf("HEAD or index changed: %s", gitOut(t, dir, "status", "--porcelain"))
	}
	if got := gitOut(t, dir, "show", commit+":new.go"); got != "package m" {
		t.Fatalf("new.go in checkpoint = %q", got)
	}
	if !strings.Contains(gitOut(t, dir, "show", commit+":m.go"), "Mul") {
		t.Fatal("checkpoint lacks the change to m.go")
	}
}

func TestSaveCheckpointNeedsGit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package m\n")
	if _, err := saveCheckpoint(context.Background(), workspace(t, dir), checkpointRef("t1", 1)); err == nil {
		t.Fatal("saved a checkpoint outside git")
	}
}

func TestRestoreCheckpoint(t *testing.T) {
	dir := goRepo(t, nil)
	sb := workspace(t, dir)
	saved := mathGo + "\nfunc Mul(a, b int) int { return a * b }\n"
	writeFile(t, filepath.Join(dir, "m.go"), saved)
	writeFile(t, filepath.Join(dir, "new.go"), "package m\n")
	commit, err := saveCheckpoint(context.Background(), sb, checkpointRef("t1", 1))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "m.go"), "package m\n// broken\n")
	writeFile(t, filepath.Join(dir, "later.go"), "package m\n")
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	restored, removed, err := restoreCheckpoint(context.Background(), sb, commit)
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "m.go")) != saved || readFile(t, filepath.Join(dir, "README.md")) != "m\n" || readFile(t, filepath.Join(dir, "new.go")) != "package m\n" {
		t.Fatal("files not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "later.go")); !os.IsNotExist(err) {
		t.Fatalf("later.go still there: %v", err)
	}
	slices.Sort(restored)
	if !slices.Equal(restored, []string{"README.md", "m.go"}) || !slices.Equal(removed, []string{"later.go"}) {
		t.Fatalf("restored = %q, removed = %q", restored, removed)
	}
}

// runCheckpoint runs call through the manager with history as the state.
func runCheckpoint(t *testing.T, m CheckpointManager, history []llm.Message, call llm.ToolCall, result string) string {
	t.Helper()
	res, err := m.WrapToolCall(context.Background(), &core.ToolCallRequest{ToolCall: call, State: &rt.State{Messages: history},
		Runtime: &core.Runtime{Config: rt.RunConfig{ThreadID: "t1"}}},
		func(context.Context, *core.ToolCallRequest) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(result)}}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res)
}

func TestCheckpointManagerSavesAfterProgress(t *testing.T) {
	m := CheckpointManager{Workspace: workspace(t, goRepo(t, nil))}
	msgs := transcript(failing("TestA", "TestB"), editN(0), failing("TestB"))
	history, check := msgs[:len(msgs)-2], msgs[len(msgs)-3].ToolCalls[0]
	text := runCheckpoint(t, m, history, check, goFailure("TestB"))
	if !strings.Contains(text, "\n\n"+checkpointMarker+` {"id":"cp1"`) || !strings.Contains(text, "progressed") {
		t.Fatalf("result:\n%s", text)
	}
	if stripNotes(text) != goFailure("TestB") {
		t.Fatalf("stripNotes left:\n%s", stripNotes(text))
	}
	if text := runCheckpoint(t, m, history, check, goFailure("TestA", "TestB", "TestC")); strings.Contains(text, checkpointMarker) {
		t.Fatalf("saved after a regression:\n%s", text)
	}
}

func TestCheckpointManagerSavesBeforeTheFirstEdit(t *testing.T) {
	m := CheckpointManager{Workspace: workspace(t, goRepo(t, nil))}
	msgs := transcript(failing("TestA"), editN(0))
	history, edit := msgs[:len(msgs)-2], msgs[len(msgs)-3].ToolCalls[0]
	text := runCheckpoint(t, m, history, edit, "Successfully replaced 1 instance(s)")
	if !strings.Contains(text, checkpointMarker) || !strings.Contains(text, "before the first edit") {
		t.Fatalf("result:\n%s", text)
	}
	noted := append(slices.Clone(history), llm.NewToolMessage(edit.ID, edit.Name, text))
	second := llm.ToolCall{ID: "e2", Name: "edit_file", Args: map[string]any{"file_path": "/b.go"}}
	noted = append(noted, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{second}})
	if text := runCheckpoint(t, m, noted, second, "Successfully replaced 1 instance(s)"); strings.Contains(text, checkpointMarker) {
		t.Fatalf("saved before the second edit:\n%s", text)
	}
}

func TestCheckpointManagerOutsideGitAddsNothing(t *testing.T) {
	dir := t.TempDir()
	m := CheckpointManager{Workspace: workspace(t, dir)}
	msgs := transcript(failing("TestA"), editN(0))
	if text := runCheckpoint(t, m, msgs[:len(msgs)-2], msgs[len(msgs)-3].ToolCalls[0], "ok"); text != "ok" {
		t.Fatalf("result = %q", text)
	}
}

func rollbackCall(t *testing.T, m CheckpointManager, msgs []llm.Message, id string) (*tool.Result, error) {
	t.Helper()
	tools := m.Tools()
	if len(tools) != 1 || tools[0].Name() != RollbackToolName {
		t.Fatalf("tools = %v", tools)
	}
	return tools[0].Invoke(context.Background(), &tool.Runtime{ToolCallID: "rb", State: &rt.State{Messages: msgs}}, map[string]any{"checkpoint": id})
}

func TestRollbackToolRestoresANotedCheckpoint(t *testing.T) {
	dir := goRepo(t, nil)
	m := CheckpointManager{Workspace: workspace(t, dir)}
	writeFile(t, filepath.Join(dir, "m.go"), mathGo+"// good\n")
	msgs := transcript(failing("TestA", "TestB"), editN(0), failing("TestB"))
	at := len(msgs) - 2
	text := runCheckpoint(t, m, msgs[:at], msgs[at-1].ToolCalls[0], goFailure("TestB"))
	msgs[at] = llm.NewToolMessage(msgs[at].ToolCallID, msgs[at].Name, text)
	writeFile(t, filepath.Join(dir, "m.go"), "package m\n// worse\n")

	res, err := rollbackCall(t, m, msgs, "cp1")
	if err != nil || res.IsError {
		t.Fatalf("rollback: %v %+v", err, res)
	}
	if got := readFile(t, filepath.Join(dir, "m.go")); got != mathGo+"// good\n" {
		t.Fatalf("m.go = %q", got)
	}
	out := resultText(res)
	for _, want := range []string{"cp1", "m.go", "read"} {
		if !strings.Contains(out, want) {
			t.Fatalf("result lacks %q: %s", want, out)
		}
	}
	if _, err := rollbackCall(t, m, msgs, "cp9"); err == nil || !strings.Contains(err.Error(), "cp1") {
		t.Fatalf("unknown checkpoint: %v", err)
	}
}

func TestStuckNoteOffersTheLatestCheckpoint(t *testing.T) {
	note := checkpointNoteText(checkpointNote{ID: "cp2", Commit: "abc", Reason: "attempt 1: progressed"})
	msgs := transcript(append([]step{hypo}, attempts(3)...)...)
	msgs[4] = llm.NewToolMessage(msgs[4].ToolCallID, msgs[4].Name, msgs[4].Text()+note)
	history, check := msgs[:len(msgs)-2], msgs[len(msgs)-3].ToolCalls[0]
	res, _ := runStuck(t, StuckDetector{}, history, check, goFailure("TestA"))
	if text := resultText(res); !strings.Contains(text, "roll back to cp2") {
		t.Fatalf("note lacks the checkpoint:\n%s", text)
	}
}

func TestRepairStateListsCheckpoints(t *testing.T) {
	msgs := transcript(failing("TestA", "TestB"), editN(0), failing("TestB"))
	at := len(msgs) - 2
	msgs[at] = llm.NewToolMessage(msgs[at].ToolCallID, msgs[at].Name, msgs[at].Text()+checkpointNoteText(checkpointNote{ID: "cp1", Commit: "abc", Reason: "attempt 1: progressed"}))
	text := renderRepairState(RepairRecord{}, deriveRepair(msgs))
	if !strings.Contains(text, `checkpoints: [{id: cp1, reason: "attempt 1: progressed"}]`) {
		t.Fatalf("repair state:\n%s", text)
	}
}

func TestRollbackNeedsApproval(t *testing.T) {
	if _, ok := DefaultInterruptOn()[RollbackToolName]; !ok {
		t.Fatal("rollback runs without review")
	}
}

func TestAgentRollsBackToACheckpoint(t *testing.T) {
	dir := goRepo(t, map[string]string{
		"a.go":      "package m\n\nfunc Two() int { return 3 }\n\nfunc Three() int { return 4 }\n",
		"a_test.go": "package m\n\nimport \"testing\"\n\nfunc TestTwo(t *testing.T) { if Two() != 2 { t.Fatal(\"two\") } }\n\nfunc TestThree(t *testing.T) { if Three() != 3 { t.Fatal(\"three\") } }\n",
	})
	call := func(id, name string, args map[string]any) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}}}
	}
	test := func(id string) llm.Message { return call(id, "execute", map[string]any{"command": "go test ./..."}) }
	model := llm.NewFakeModel(
		test("t0"),
		call("r", "read_file", map[string]any{"file_path": "/a.go"}),
		call("e1", "edit_file", map[string]any{"file_path": "/a.go", "old_string": "return 3", "new_string": "return 2"}),
		test("t1"), // TestTwo fixed: checkpoint cp2
		call("e2", "edit_file", map[string]any{"file_path": "/a.go", "old_string": "Two() int { return 2 }", "new_string": "Two() int { return 7 }"}),
		test("t2"), // regressed
		call("rb", RollbackToolName, map[string]any{"checkpoint": "cp2"}),
		test("t3"),
		llm.NewAssistantMessage("Rolled back."),
	)
	a, err := CreateCLIAgent(context.Background(), Options{Model: model, Cwd: dir, Backend: NewBackend(dir), AutoApprove: true, NoWebTools: true, NoVerifyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), core.Input{Messages: []llm.Message{llm.NewUserMessage("fix Two and Three")}}, rt.RunConfig{ThreadID: "th"})
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	for _, m := range st.Messages {
		if m.Role == llm.RoleTool {
			results[m.ToolCallID] = m.Text()
		}
	}
	if !strings.Contains(results["e1"], `"id":"cp1"`) || !strings.Contains(results["t1"], `"id":"cp2"`) {
		t.Fatalf("checkpoints missing:\ne1: %s\nt1: %s", results["e1"], results["t1"])
	}
	if got := readFile(t, filepath.Join(dir, "a.go")); !strings.Contains(got, "return 2") {
		t.Fatalf("a.go = %q\nrollback: %s", got, results["rb"])
	}
	if !strings.Contains(results["t3"], "TestThree") || strings.Contains(results["t3"], "TestTwo") {
		t.Fatalf("t3 = %s", results["t3"])
	}
	if out, _ := exec.Command("git", "-C", dir, "for-each-ref", "refs/quill/checkpoints/th").Output(); strings.Count(string(out), "\n") != 2 {
		t.Fatalf("refs:\n%s", out)
	}
}
