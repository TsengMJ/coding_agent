package server

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/browser"
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// fakePlaywright is a workspace browser: navigate, screenshot and click.
type fakePlaywright struct {
	mu     sync.Mutex
	cfgs   []mcp.ServerConfig
	clicks []map[string]any
	closed int
	err    error
}

func (f *fakePlaywright) connect(_ context.Context, cfgs []mcp.ServerConfig) (mcp.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfgs = append(f.cfgs, cfgs...)
	if f.err != nil {
		return nil, f.err
	}
	named := func(name string, fn func(map[string]any) *tool.Result) tool.Tool {
		return &tool.Func{ToolName: "mcp__playwright__" + name, ToolSchema: tool.Schema{"type": "object"},
			Fn: func(_ context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
				return fn(args), nil
			}}
	}
	return &closingMCP{fakeMCP: &fakeMCP{tools: []tool.Tool{
		named("browser_navigate", func(map[string]any) *tool.Result {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("navigated")}}
		}),
		named(browser.ScreenshotTool, func(map[string]any) *tool.Result {
			return &tool.Result{Content: []llm.ContentBlock{{Type: llm.BlockImage, MimeType: "image/jpeg",
				Base64: base64.StdEncoding.EncodeToString([]byte("frame"))}}}
		}),
		named(browser.ClickTool, func(args map[string]any) *tool.Result {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.clicks = append(f.clicks, args)
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("clicked")}}
		}),
	}}, onClose: func() { f.mu.Lock(); f.closed++; f.mu.Unlock() }}, nil
}

type closingMCP struct {
	*fakeMCP
	onClose func()
}

func (c *closingMCP) Close() error { c.onClose(); return nil }

func newBrowserWorkspace(t *testing.T, fp *fakePlaywright, replies ...llm.Message) (*Service, *workspace.Workspace) {
	t.Helper()
	svc := newWorkspaceServiceWith(t, func(c *Config) { c.WorkspaceBrowser, c.ConnectMCP = true, fp.connect }, replies...)
	ws, err := svc.CreateWorkspace(as("alice"), workspace.Spec{RepoURL: gitRepo(t, map[string]string{"a.txt": "a"})})
	if err != nil {
		t.Fatal(err)
	}
	return svc, ws
}

func TestWorkspaceRunsDriveABrowserInTheirContainer(t *testing.T) {
	fp := &fakePlaywright{}
	svc, ws := newBrowserWorkspace(t, fp,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "n", Name: "mcp__playwright__browser_navigate",
			Args: map[string]any{"url": "http://localhost:5173"}}}},
		llm.NewAssistantMessage("done"),
	)
	events, err := collectAs(t, as("alice"), svc, wsReq("t1", ws.ID, "open the app"))
	if err != nil {
		t.Fatal(err)
	}
	var got string
	for _, m := range messages(events) {
		if m.ToolCallID == "n" {
			got = m.Text()
		}
	}
	if got != "navigated" {
		t.Fatalf("navigate result = %q", got)
	}
	if len(fp.cfgs) != 1 || !slices.Contains(fp.cfgs[0].Args, workspace.ContainerName(ws.ID)) {
		t.Fatalf("browser MCP = %+v, want docker exec into the workspace", fp.cfgs)
	}

	h := NewHandler(svc)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/threads/t1/browser/screenshot", nil).WithContext(as("alice")))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/jpeg" || rec.Body.String() != "frame" {
		t.Fatalf("screenshot = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/threads/t1/browser/click", strings.NewReader(`{"x":120,"y":45.5}`)).WithContext(as("alice")))
	if rec.Code != http.StatusNoContent || len(fp.clicks) != 1 || fp.clicks[0]["x"] != 120.0 || fp.clicks[0]["y"] != 45.5 {
		t.Fatalf("click = %d, clicks %v", rec.Code, fp.clicks)
	}
	if _, err := svc.BrowserScreenshot(as("bob"), "t1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob's screenshot err = %v", err)
	}

	if err := svc.StopWorkspace(as("alice"), ws.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BrowserScreenshot(as("alice"), "t1"); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("screenshot after stop err = %v", err)
	}
}

func TestWorkspaceRunGoesOnWithoutABrowserThatCannotStart(t *testing.T) {
	fp := &fakePlaywright{err: errors.New("npx: not found")}
	svc, ws := newBrowserWorkspace(t, fp, llm.NewAssistantMessage("fine"))
	if _, err := collectAs(t, as("alice"), svc, wsReq("t1", ws.ID, "hi")); err != nil {
		t.Fatalf("run failed because of the browser: %v", err)
	}
	if _, err := svc.BrowserScreenshot(as("alice"), "t1"); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("screenshot err = %v", err)
	}
}
