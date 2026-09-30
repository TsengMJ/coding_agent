package bench

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// hostDocker is a fake docker binary that runs `docker exec` commands on
// the host in the -w directory, so the agent's execute calls work.
func hostDocker(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
shift
while [ $# -gt 0 ]; do
  case "$1" in
    -w) shift 2 ;;
    -e) shift 2 ;;
    *) break ;;
  esac
done
shift
exec "$@"
`
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func toolCall(id, name string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: name, Args: args}},
		Usage: &llm.Usage{InputTokens: 1000, OutputTokens: 100}}
}

func TestDcodeSolverCapturesPatchAndMetrics(t *testing.T) {
	root := t.TempDir()
	task, err := LoadTask(writeTask(t, root, "instance_a"))
	if err != nil {
		t.Fatal(err)
	}
	model := llm.NewFakeModel(
		toolCall("c1", "read_file", map[string]any{"file_path": "/README"}),
		toolCall("c2", "execute", map[string]any{"command": "echo go test ./..."}),
		toolCall("c3", "write_file", map[string]any{"file_path": "/fix.txt", "content": "fixed\n"}),
		llm.NewAssistantMessage("done"),
	)
	d := newFakeDocker()
	d.execHook = func(_ string, spec ExecSpec) (ExecResult, bool) {
		if strings.Contains(strings.Join(spec.Cmd, " "), "git diff --cached") {
			return ExecResult{Stdout: []byte("diff --git a/fix.txt b/fix.txt\n")}, true
		}
		return ExecResult{}, false
	}
	work := filepath.Join(t.TempDir(), "work")
	logDir := t.TempDir()
	s := &DcodeSolver{
		NewModel:      func() (llm.ChatModel, error) { return model, nil },
		Prices:        cost.Table{"fake:fake": {Input: 1, Output: 10}},
		KeepWorkspace: true,
		DockerBinary:  hostDocker(t),
	}
	res, err := s.Solve(context.Background(), task, SolveEnv{RunID: "r", WorkDir: work, LogDir: logDir, Docker: d})
	if err != nil {
		t.Fatal(err)
	}
	if res.Category != "" || res.Patch != "diff --git a/fix.txt b/fix.txt\n" {
		t.Fatalf("result %+v", res)
	}
	spec := d.runSpecs[0]
	ws := filepath.Join(work, "workspace")
	if !spec.NoNetwork || spec.Mounts[ws] != "/app" || spec.Workdir != "/app" {
		t.Fatalf("agent container %+v", spec)
	}
	if data, err := os.ReadFile(filepath.Join(ws, "fix.txt")); err != nil || string(data) != "fixed\n" {
		t.Fatalf("file tools should write the mounted workspace: %q %v", data, err)
	}
	if data, _ := os.ReadFile(filepath.Join(ws, ".git", "info", "exclude")); !strings.Contains(string(data), "/.deepagents/") {
		t.Fatal("agent artifacts not excluded from the patch")
	}
	m := res.Metrics
	if m.Steps != 4 || m.ToolCalls["write_file"] != 1 || m.ExecuteCalls != 1 || m.TestRuns != 1 || m.FilesRead != 1 {
		t.Fatalf("metrics %+v", m)
	}
	if m.FirstEditStep == nil || *m.FirstEditStep != 3 || m.PeakContextTokens != 1000 || m.CostUSD == nil || m.AgentMS <= 0 {
		t.Fatalf("metrics %+v", m)
	}
	if _, err := os.Stat(res.TracePath); err != nil {
		t.Fatalf("trace: %v", err)
	}
	for _, tools := range model.Requests()[0].Tools {
		if tools.Name == "fetch_url" || tools.Name == "web_search" {
			t.Fatal("agent must be offline")
		}
	}
	prompt := model.Requests()[0].Messages[0].Text()
	if !strings.Contains(prompt, "where `/` is `/app`") || strings.Contains(prompt, work) {
		t.Fatal("prompt should describe /app, not the host workspace")
	}
}

func TestDcodeSolverFallsBackToTestbed(t *testing.T) {
	root := t.TempDir()
	task, _ := LoadTask(writeTask(t, root, "instance_a"))
	d := &copyOutFailDocker{fakeDocker: newFakeDocker(), fail: "/app/."}
	s := &DcodeSolver{NewModel: func() (llm.ChatModel, error) { return llm.NewFakeModel(llm.NewAssistantMessage("done")), nil },
		DockerBinary: hostDocker(t)}
	if _, err := s.Solve(context.Background(), task, SolveEnv{RunID: "r", WorkDir: filepath.Join(t.TempDir(), "w"), Docker: d}); err != nil {
		t.Fatal(err)
	}
	for _, target := range d.runSpecs[0].Mounts {
		if target != "/testbed" {
			t.Fatalf("mounted at %s", target)
		}
	}
}

type copyOutFailDocker struct {
	*fakeDocker
	fail string
}

func (c *copyOutFailDocker) CopyOut(ctx context.Context, image, src, dst string) error {
	if src == c.fail {
		return errors.New("no such path")
	}
	return c.fakeDocker.CopyOut(ctx, image, src, dst)
}

func TestMeteredModelStopsAtBudget(t *testing.T) {
	inner := llm.NewFakeModel(llm.Message{Role: llm.RoleAssistant, Usage: &llm.Usage{InputTokens: 1_000_000}},
		llm.NewAssistantMessage("never"))
	m := &meteredModel{ChatModel: inner, prices: cost.Table{"fake:fake": {Input: 2}}, limit: 1}
	if _, err := m.Generate(context.Background(), llm.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Generate(context.Background(), llm.ChatRequest{}); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("err %v", err)
	}
}

func TestClassifyAgentError(t *testing.T) {
	expired, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	live := context.Background()
	failing := &meteredModel{modelFailed: true}
	ok := &meteredModel{}
	cases := []struct {
		err  error
		ctx  context.Context
		m    *meteredModel
		want string
	}{
		{context.DeadlineExceeded, expired, ok, CategoryAgentTimeout},
		{ErrBudgetExceeded, live, ok, CategoryBudget},
		{core.ErrRecursionLimit, live, ok, CategoryBudget},
		{errors.New("429"), live, failing, CategoryModelError},
		{errors.New("boom"), live, ok, CategoryAgentError},
	}
	for _, tc := range cases {
		if got := classifyAgentError(tc.err, tc.ctx, tc.m); got != tc.want {
			t.Errorf("%v: got %s want %s", tc.err, got, tc.want)
		}
	}
}

func TestMetricsFromTraceSkipsSummarizationCalls(t *testing.T) {
	summary := llm.NewUserMessage("summary of earlier work")
	summary.Metadata = map[string]any{"lc_source": "summarization"}
	toolMsg := llm.WithTiming(llm.Message{Role: llm.RoleTool, ToolCallID: "e1"}, llm.Timing{Start: time.Now(), Duration: 2 * time.Second})
	calls := []trace.LLMCallDetail{
		{LLMCall: trace.LLMCall{ToolCount: 3, DurationMs: 100, Model: "fake:fake", Usage: &llm.Usage{InputTokens: 500}},
			Response: &llm.Message{ToolCalls: []llm.ToolCall{{ID: "e1", Name: "execute", Args: map[string]any{"command": "npx jest src"}}}}},
		{LLMCall: trace.LLMCall{ToolCount: 0, DurationMs: 50, Usage: &llm.Usage{InputTokens: 9000}, Namespace: []string{"task:x"}}},
		{LLMCall: trace.LLMCall{ToolCount: 0, DurationMs: 50, Usage: &llm.Usage{InputTokens: 800}}},
		{LLMCall: trace.LLMCall{ToolCount: 3, DurationMs: 100, Usage: &llm.Usage{InputTokens: 700}},
			Messages: []llm.Message{summary, toolMsg}},
	}
	m := MetricsFromTrace(calls, cost.Table{})
	if m.Steps != 2 || m.LLMCalls != 4 || m.ModelMS != 300 || m.Summarizations != 1 || m.TestRuns != 1 {
		t.Fatalf("metrics %+v", m)
	}
	if m.PeakContextTokens != 800 || m.ExecuteMS != 2000 || m.ToolMS != 2000 || m.CostUSD != nil {
		t.Fatalf("metrics %+v", m)
	}
}
