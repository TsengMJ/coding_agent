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
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/delivery"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
)

const githubRepoURL = "https://github.com/acme/app"

// fakeGitHub records the pull requests opened through it.
type fakeGitHub struct {
	*httptest.Server
	mu       sync.Mutex
	requests []map[string]any
	tokens   []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/repos/acme/app/pulls" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.tokens = append(f.tokens, r.Header.Get("Authorization"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"number":7,"html_url":"https://github.com/acme/app/pull/7","state":"open"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

// newDeliveryService serves workspaces of githubRepoURL that are cloned
// from, and pushed to, a local bare repository.
func newDeliveryService(t *testing.T, replies ...llm.Message) (*Service, string, *fakeGitHub) {
	t.Helper()
	src := gitRepo(t, map[string]string{"README.md": "hello\n"})
	bare := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "clone", "-q", "--bare", src, bare).CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v %s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, []byte(fakeDockerScript), 0o755); err != nil {
		t.Fatal(err)
	}
	store := storage.NewMemory()
	m, err := workspace.NewManager(workspace.Config{
		Root: t.TempDir(), Store: store.Workspaces, Docker: workspace.CLIDocker{Binary: bin},
		Clone: func(ctx context.Context, spec workspace.Spec, dir, _ string) error {
			spec.RepoURL = "file://" + bare
			return workspace.GitClone(ctx, spec, dir, "")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	gh := newFakeGitHub(t)
	svc, err := New(context.Background(), Config{
		Storage: store, Model: llm.NewFakeModel(replies...), NoVerifyGate: true,
		Workspaces: m, RequireWorkspace: true,
		GitHub:  &delivery.GitHub{APIURL: gh.URL},
		PushURL: func(string) string { return "file://" + bare },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc, bare, gh
}

func alice() context.Context {
	return auth.WithUser(context.Background(), auth.User{ID: "alice", Name: "Alice", GitHubToken: "gh-alice"})
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestDiffAndPullRequestOfAWorkspaceThread(t *testing.T) {
	svc, bare, gh := newDeliveryService(t, llm.NewAssistantMessage("ok"))
	ws, err := svc.CreateWorkspace(alice(), workspace.Spec{RepoURL: githubRepoURL})
	if err != nil {
		t.Fatal(err)
	}
	if err := runAs(alice(), svc, wsReq("t1", ws.ID, "hi")); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.GetWorkspace(alice(), ws.ID)
	if err := os.WriteFile(filepath.Join(got.Dir, "feature.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := svc.GetDiff(alice(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || d.Files[0].Path != "feature.txt" || d.Files[0].Status != delivery.StatusAdded {
		t.Fatalf("diff files = %+v", d.Files)
	}
	if _, err := svc.GetDiff(as("bob"), "t1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob diff err = %v", err)
	}

	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/threads/t1/pr", strings.NewReader(`{"title":"Add feature","body":"Adds feature.txt"}`))
	h.ServeHTTP(rec, req.WithContext(alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /pr = %d %s", rec.Code, rec.Body)
	}
	var res PRResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || res.PullRequest.Number != 7 || res.Commit.Branch != "quill/t1" {
		t.Fatalf("result = %s", rec.Body)
	}
	if head := gitOut(t, bare, "rev-parse", "refs/heads/quill/t1"); head != res.Commit.Head {
		t.Fatalf("remote quill/t1 = %s, want %s", head, res.Commit.Head)
	}
	if author := gitOut(t, bare, "log", "-1", "--format=%an <%ae>", "quill/t1"); author != "Alice <alice@users.noreply.github.com>" {
		t.Fatalf("author = %q", author)
	}
	if len(gh.requests) != 1 || gh.tokens[0] != "Bearer gh-alice" ||
		gh.requests[0]["head"] != "quill/t1" || gh.requests[0]["base"] != "main" || gh.requests[0]["title"] != "Add feature" {
		t.Fatalf("GitHub got %v with %v", gh.requests, gh.tokens)
	}
	if !strings.Contains(gh.requests[0]["body"].(string), "Adds feature.txt") {
		t.Fatalf("PR body = %v", gh.requests[0]["body"])
	}
	events, _ := svc.AuditLog(alice(), 10)
	if len(events) == 0 || events[0].Action != audit.ActionPullRequest || events[0].Detail["url"] != "https://github.com/acme/app/pull/7" {
		t.Fatalf("latest audit event = %+v", events)
	}

	if _, err := svc.CreatePR(alice(), "t1", PRRequest{Title: "again"}); err != nil {
		t.Fatalf("re-delivering the pushed branch: %v", err)
	}
	if _, err := svc.CreatePR(as("alice"), "t1", PRRequest{Title: "x"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("without a GitHub token err = %v", err)
	}
}

func TestCreatePRRefusesLocalThreads(t *testing.T) {
	svc := newWorkspaceService(t, false, llm.NewAssistantMessage("hi"))
	if _, err := collect(t, svc, userReq("local", t.TempDir(), "hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePR(context.Background(), "local", PRRequest{Title: "x"}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v", err)
	}
}

func TestCreatePRToolNeedsApprovalEvenWhenAutoApproved(t *testing.T) {
	svc, bare, _ := newDeliveryService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "w", Name: "write_file",
			Args: map[string]any{"file_path": "/feature.txt", "content": "new\n"}}}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "pr", Name: createPRToolName,
			Args: map[string]any{"title": "Add feature"}}}},
		llm.NewAssistantMessage("opened"),
	)
	ws, err := svc.CreateWorkspace(alice(), workspace.Spec{RepoURL: githubRepoURL})
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for ev, err := range svc.Run(alice(), wsReq("t1", ws.ID, "make a PR")) {
		if err != nil {
			t.Fatal(err)
		}
		last = ev
	}
	if last.Kind != EventInterrupt || len(last.Interrupts) != 1 || last.Interrupts[0].ActionRequests[0].Name != createPRToolName {
		t.Fatalf("last event = %+v, want a create_pr approval", last)
	}
	if !strings.Contains(last.Interrupts[0].ActionRequests[0].Description, "Add feature") {
		t.Fatalf("approval prompt = %q", last.Interrupts[0].ActionRequests[0].Description)
	}

	resume := RunRequest{ThreadID: "t1", WorkspaceID: ws.ID, AutoApprove: true,
		Resume: &Resume{Decisions: []hitl.Decision{{Type: hitl.DecisionApprove}}}}
	var result string
	for ev, err := range svc.Run(alice(), resume) {
		if err != nil {
			t.Fatal(err)
		}
		if ev.Message != nil && ev.Message.ToolCallID == "pr" {
			result = ev.Message.Text()
		}
	}
	if !strings.Contains(result, "Opened pull request #7") {
		t.Fatalf("create_pr result = %q", result)
	}
	if got := gitOut(t, bare, "show", "quill/t1:feature.txt"); got != "new" {
		t.Fatalf("feature.txt on the remote = %q", got)
	}
}
