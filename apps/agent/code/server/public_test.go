package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func runAs(ctx context.Context, srv Server, req RunRequest) error {
	for _, err := range srv.Run(ctx, req) {
		if err != nil {
			return err
		}
	}
	return nil
}

func as(user string) context.Context {
	return auth.WithUser(context.Background(), auth.User{ID: user})
}

// aliceThread has alice run thread t1 in her own workspace.
func aliceThread(t *testing.T, svc *Service) *workspace.Workspace {
	t.Helper()
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	ws, err := svc.CreateWorkspace(as("alice"), workspace.Spec{RepoURL: repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := runAs(as("alice"), svc, wsReq("t1", ws.ID, "hi")); err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestThreadsBelongToTheirOwner(t *testing.T) {
	svc := newWorkspaceService(t, true, llm.NewAssistantMessage("hi"), llm.NewAssistantMessage("again"))
	aliceThread(t, svc)
	bob := as("bob")

	if _, err := svc.GetThread(bob, "t1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob GetThread err = %v", err)
	}
	if _, err := svc.GetHistory(bob, "t1", 0, 20); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob GetHistory err = %v", err)
	}
	if _, err := svc.GetUsage(bob, "t1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob GetUsage err = %v", err)
	}
	if _, err := svc.RenameThread(bob, "t1", "mine"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob RenameThread err = %v", err)
	}
	if list, _ := svc.ListThreads(bob, sessions.Filter{}); len(list) != 0 {
		t.Fatalf("bob lists %d threads", len(list))
	}
	if list, _ := svc.ListThreads(as("alice"), sessions.Filter{}); len(list) != 1 {
		t.Fatalf("alice lists %d threads, want 1", len(list))
	}
	if _, err := svc.GetThread(as("alice"), "t1"); err != nil {
		t.Fatalf("alice GetThread err = %v", err)
	}
}

func TestSignedInUsersCannotTakeOverOwnerlessThreads(t *testing.T) {
	svc := newWorkspaceService(t, false, llm.NewAssistantMessage("hi"))
	if _, err := collect(t, svc, userReq("local", t.TempDir(), "hi")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetThread(as("alice"), "local"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("alice GetThread err = %v", err)
	}
	if _, err := svc.GetThread(context.Background(), "local"); err != nil {
		t.Fatalf("local GetThread err = %v", err)
	}
}

func TestRunsAndWorkspacesAreAudited(t *testing.T) {
	svc := newWorkspaceService(t, true, llm.NewAssistantMessage("hi"))
	ws := aliceThread(t, svc)
	events, err := svc.AuditLog(as("alice"), 10)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, ev := range events {
		actions = append(actions, ev.Action)
		if ev.User != "alice" || ev.WorkspaceID != ws.ID {
			t.Fatalf("event = %+v", ev)
		}
	}
	if strings.Join(actions, ",") != "run,workspace_create" {
		t.Fatalf("actions = %v", actions)
	}
	if events[0].ThreadID != "t1" {
		t.Fatalf("run event thread = %q", events[0].ThreadID)
	}
	if others, _ := svc.AuditLog(as("bob"), 10); len(others) != 0 {
		t.Fatalf("bob sees %d events", len(others))
	}
}

func TestDailyLimitRefusesRuns(t *testing.T) {
	svc := newWorkspaceService(t, true, llm.NewAssistantMessage("hi"))
	repo := gitRepo(t, map[string]string{"a.txt": "a"})
	ws, err := svc.CreateWorkspace(as("alice"), workspace.Spec{RepoURL: repo})
	if err != nil {
		t.Fatal(err)
	}
	svc.dailyLimit = 1
	if err := svc.audit.Record(context.Background(), audit.Event{User: "alice", Action: audit.ActionRun, CostUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	err = runAs(as("alice"), svc, wsReq("t1", ws.ID, "hi"))
	if !errors.Is(err, ErrQuotaExceeded) || statusFor(err) != http.StatusTooManyRequests {
		t.Fatalf("over-limit run err = %v (status %d)", err, statusFor(err))
	}
}

// publicHandler serves svc with one signed-in session per user name.
func publicHandler(t *testing.T, svc *Service, users ...string) (http.Handler, map[string]string) {
	t.Helper()
	cipher, err := auth.NewCipher(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewMemoryStore()
	tokens := map[string]string{}
	ctx := context.Background()
	for _, u := range users {
		sealed, _ := cipher.Seal("gh-" + u)
		if err := store.PutUser(ctx, auth.Record{ID: u, Name: u, EncryptedToken: sealed}); err != nil {
			t.Fatal(err)
		}
		tok, _ := auth.NewSessionToken()
		if err := store.CreateSession(ctx, auth.HashToken(tok), u, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		tokens[u] = tok
	}
	h, err := NewPublicHandler(PublicOptions{
		API:            NewHandler(svc),
		Auth:           &auth.Service{GitHub: &auth.GitHub{ClientID: "cid"}, Store: store, Cipher: cipher, Allowed: users},
		AllowedOrigins: []string{"app://quill"},
		Info: func(context.Context) (PublicInfo, error) {
			return PublicInfo{Model: "fake:fake", Workspaces: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h, tokens
}

func TestPublicHandlerRequiresLogin(t *testing.T) {
	svc := newWorkspaceService(t, true, llm.NewAssistantMessage("hi"))
	aliceThread(t, svc)
	h, tokens := publicHandler(t, svc, "alice", "bob")
	do := func(method, path, token, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Host = "quill.example.com"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("GET", "/healthz", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := do("GET", "/threads", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("threads without login = %d", rec.Code)
	}
	if rec := do("GET", "/threads", "qs_wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("threads with a bad token = %d", rec.Code)
	}
	rec := do("GET", "/info", tokens["alice"], "")
	var info PublicInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); rec.Code != http.StatusOK || err != nil || info.User.ID != "alice" {
		t.Fatalf("info = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "gh-alice") {
		t.Fatalf("info leaks the GitHub token: %s", rec.Body)
	}
	if rec := do("GET", "/threads/t1", tokens["bob"], ""); rec.Code != http.StatusForbidden {
		t.Fatalf("bob reads alice's thread = %d", rec.Code)
	}
	if rec := do("GET", "/threads/t1/events", tokens["bob"], ""); rec.Code != http.StatusForbidden {
		t.Fatalf("bob follows alice's run = %d", rec.Code)
	}
	if rec := do("POST", "/threads/t1/cancel", tokens["bob"], ""); rec.Code != http.StatusForbidden {
		t.Fatalf("bob cancels alice's run = %d", rec.Code)
	}
	if rec := do("GET", "/threads/t1", tokens["alice"], ""); rec.Code != http.StatusOK {
		t.Fatalf("alice reads her thread = %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/audit", tokens["alice"], ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"run"`) {
		t.Fatalf("audit = %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/threads", tokens["alice"], "https://evil.example"); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin = %d", rec.Code)
	}
	rec = do("OPTIONS", "/threads", "", "app://quill")
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "app://quill" ||
		!strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight = %d %v", rec.Code, rec.Header())
	}
}

func TestPublicHandlerNeedsAnAllowList(t *testing.T) {
	svc := newWorkspaceService(t, true)
	cipher, _ := auth.NewCipher(strings.Repeat("ab", 32))
	_, err := NewPublicHandler(PublicOptions{
		API:  NewHandler(svc),
		Auth: &auth.Service{GitHub: &auth.GitHub{ClientID: "cid"}, Store: auth.NewMemoryStore(), Cipher: cipher},
		Info: func(context.Context) (PublicInfo, error) { return PublicInfo{}, nil },
	})
	if err == nil {
		t.Fatal("handler without an allow list was accepted")
	}
}
