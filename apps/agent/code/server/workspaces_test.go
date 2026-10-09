package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/process"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// fakeDockerScript stands in for the docker CLI: one container whose
// bind-mounted host directory is remembered in $state, and exec runs the
// command there on the host.
const fakeDockerScript = `#!/bin/sh
state="$(dirname "$0")/state"
cmd=$1; shift
case "$cmd" in
inspect) [ -f "$state" ] && { echo true; exit 0; }; echo "No such container" >&2; exit 1 ;;
run)
  while [ $# -gt 0 ]; do
    if [ "$1" = "-v" ]; then echo "${2%%:/workspace}" > "$state"; fi
    shift
  done
  echo id ;;
start|stop|version) ;;
rm) rm -f "$state" ;;
exec)
  while [ $# -gt 0 ]; do case "$1" in -w|-e) shift 2 ;; -i) shift ;; *) break ;; esac; done
  shift
  if [ "$1" = git ] && [ "$2" = config ]; then exit 0; fi
  cd "$(cat "$state")" && exec "$@" ;;
*) echo "unexpected docker $cmd" >&2; exit 2 ;;
esac
`

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return dir
}

func newWorkspaceService(t *testing.T, require bool, replies ...llm.Message) *Service {
	t.Helper()
	return newWorkspaceServiceWith(t, func(c *Config) { c.RequireWorkspace = require }, replies...)
}

// newWorkspaceServiceWith is newWorkspaceService with configure applied to
// the service config.
func newWorkspaceServiceWith(t *testing.T, configure func(*Config), replies ...llm.Message) *Service {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	m, err := workspace.NewManager(workspace.Config{
		Root: t.TempDir(), Store: store.Workspaces, Docker: workspace.CLIDocker{Binary: bin}, AllowLocalRepos: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Storage: store, Model: llm.NewFakeModel(replies...), NoVerifyGate: true, Workspaces: m}
	configure(&cfg)
	svc, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func wsReq(thread, wsID, text string) RunRequest {
	return RunRequest{ThreadID: thread, WorkspaceID: wsID, Messages: []llm.Message{llm.NewUserMessage(text)}, AutoApprove: true}
}

func TestServiceRunsInAWorkspace(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "hello\n"})
	svc := newWorkspaceService(t, true,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/notes.txt", "content": "written\n"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "x", Name: "execute",
			Args: map[string]any{"command": "cat README.md notes.txt"}}}},
		llm.NewAssistantMessage("done"),
	)
	ctx := context.Background()
	ws, err := svc.CreateWorkspace(ctx, workspace.Spec{RepoURL: repo})
	if err != nil {
		t.Fatal(err)
	}
	events, err := collect(t, svc, wsReq("t1", ws.ID, "go"))
	if err != nil {
		t.Fatal(err)
	}
	var execOut string
	for _, m := range messages(events) {
		if m.ToolCallID == "x" {
			execOut = m.Text()
		}
	}
	if !strings.Contains(execOut, "hello\nwritten\n") {
		t.Fatalf("execute output = %q", execOut)
	}
	got, err := svc.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(got.Dir, "notes.txt")); string(body) != "written\n" {
		t.Fatalf("notes.txt on the host = %q", body)
	}
	if got.State != workspace.StateRunning || got.InUse {
		t.Fatalf("workspace after run = %+v", got)
	}
	info, _ := svc.sessions.Get(ctx, "t1")
	if info == nil || info.WorkspaceID != ws.ID {
		t.Fatalf("thread info = %+v", info)
	}
}

func TestInteractiveWorkspaceRunsStartProcessesInTheContainer(t *testing.T) {
	repo := gitRepo(t, map[string]string{"README.md": "hello\n"})
	svc := newWorkspaceService(t, true,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s", Name: process.StartName,
			Args: map[string]any{"command": "echo serving; pwd > where.txt; sleep 30", "wait_seconds": 1}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "k", Name: process.StopName,
			Args: map[string]any{"id": "p1"}}}},
		llm.NewAssistantMessage("done"),
	)
	ws, err := svc.CreateWorkspace(context.Background(), workspace.Spec{RepoURL: repo})
	if err != nil {
		t.Fatal(err)
	}
	req := wsReq("t1", ws.ID, "start the server")
	req.Interactive = true
	events, err := collect(t, svc, req)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range messages(events) {
		out[m.ToolCallID] = m.Text()
	}
	if !strings.Contains(out["s"], "serving") || !strings.Contains(out["k"], "stopped") {
		t.Fatalf("start = %q, stop = %q", out["s"], out["k"])
	}
	got, _ := svc.GetWorkspace(context.Background(), ws.ID)
	where, _ := os.ReadFile(filepath.Join(got.Dir, "where.txt"))
	if real, _ := filepath.EvalSymlinks(got.Dir); strings.TrimSpace(string(where)) != real && strings.TrimSpace(string(where)) != got.Dir {
		t.Fatalf("process ran in %q, want the workspace %q", where, got.Dir)
	}
}

func TestServiceRequiresAWorkspaceWhenConfigured(t *testing.T) {
	svc := newWorkspaceService(t, true)
	if _, err := collect(t, svc, userReq("t1", t.TempDir(), "hi")); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("cwd run err = %v, want ErrBadRequest", err)
	}
}

func TestServiceKeepsAThreadInItsWorkspace(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	svc := newWorkspaceService(t, false, llm.NewAssistantMessage("one"), llm.NewAssistantMessage("two"))
	ctx := context.Background()
	a, _ := svc.CreateWorkspace(ctx, workspace.Spec{RepoURL: repo})
	b, _ := svc.CreateWorkspace(ctx, workspace.Spec{RepoURL: repo})
	if _, err := collect(t, svc, wsReq("t1", a.ID, "first")); err != nil {
		t.Fatal(err)
	}
	if _, err := collect(t, svc, wsReq("t1", b.ID, "second")); !errors.Is(err, ErrThreadCwdMismatch) {
		t.Fatalf("other workspace err = %v", err)
	}
	if _, err := collect(t, svc, userReq("t1", t.TempDir(), "third")); !errors.Is(err, ErrThreadCwdMismatch) {
		t.Fatalf("local directory err = %v", err)
	}
}

func TestWorkspaceRoutes(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	h := NewHandler(newWorkspaceService(t, true))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	if rec := do("POST", "/workspaces", `{"repo_url":"ssh://x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad spec = %d %s", rec.Code, rec.Body)
	}
	rec := do("POST", "/workspaces", `{"repo_url":"`+repo+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var ws workspace.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil || ws.ID == "" {
		t.Fatalf("create body %s: %v", rec.Body, err)
	}
	if strings.Contains(rec.Body.String(), repo+"/") || strings.Contains(rec.Body.String(), `"dir"`) {
		t.Fatalf("response exposes the host path: %s", rec.Body)
	}
	if rec := do("GET", "/workspaces", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), ws.ID) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/workspaces/"+ws.ID+"/stop", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop = %d %s", rec.Code, rec.Body)
	}
	if rec := do("DELETE", "/workspaces/"+ws.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/workspaces/"+ws.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted = %d %s", rec.Code, rec.Body)
	}
}

func TestServiceRefusesAnotherUsersWorkspace(t *testing.T) {
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	svc := newWorkspaceService(t, true, llm.NewAssistantMessage("hi"))
	alice := auth.WithUser(context.Background(), auth.User{ID: "alice"})
	bob := auth.WithUser(context.Background(), auth.User{ID: "bob"})
	ws, err := svc.CreateWorkspace(alice, workspace.Spec{RepoURL: repo})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Owner != "alice" {
		t.Fatalf("owner = %q", ws.Owner)
	}
	if _, err := svc.GetWorkspace(bob, ws.ID); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("bob Get err = %v", err)
	}
	if list, _ := svc.ListWorkspaces(bob); len(list) != 0 {
		t.Fatalf("bob sees %d workspaces", len(list))
	}
	for _, err := range svc.Run(bob, wsReq("t1", ws.ID, "hi")) {
		if !errors.Is(err, workspace.ErrForbidden) {
			t.Fatalf("bob run err = %v", err)
		}
	}
	if err := svc.DeleteWorkspace(bob, ws.ID); !errors.Is(err, workspace.ErrForbidden) {
		t.Fatalf("bob delete err = %v", err)
	}
	if err := svc.DeleteWorkspace(alice, ws.ID); err != nil {
		t.Fatal(err)
	}
}
