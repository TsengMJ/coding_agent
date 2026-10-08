package server

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// fakeConnector records the browser MCP servers it was asked to start.
type fakeConnector struct {
	cfgs     []mcp.ServerConfig
	sessions []*fakeMCP
	err      error
}

func (f *fakeConnector) connect(_ context.Context, cfgs []mcp.ServerConfig) (mcp.Session, error) {
	f.cfgs = append(f.cfgs, cfgs...)
	if f.err != nil {
		return nil, f.err
	}
	s := &fakeMCP{tools: []tool.Tool{&tool.Func{ToolName: "mcp__" + cfgs[0].Name + "__navigate", ToolSchema: tool.Schema{"type": "object"},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("navigated")}}, nil
		}}}}
	f.sessions = append(f.sessions, s)
	return s, nil
}

func newBrowsers(t *testing.T) (*browserManager, *fakeConnector) {
	t.Helper()
	fc := &fakeConnector{}
	b := newBrowserManager("playwright", fc.connect)
	t.Cleanup(func() { _ = b.Close() })
	return b, fc
}

func TestBrowserToolsNeedAnEndpoint(t *testing.T) {
	b, fc := newBrowsers(t)
	bt, err := b.toolsFor(context.Background(), "")
	if err != nil || bt.kind != "" || len(bt.tools) != 0 || len(fc.cfgs) != 0 {
		t.Fatalf("tools = %+v, err = %v, connects = %d", bt, err, len(fc.cfgs))
	}
}

func TestBrowserToolsUseTheDefaultKind(t *testing.T) {
	b, fc := newBrowsers(t)
	if err := b.SetEndpoint("http://127.0.0.1:9333"); err != nil {
		t.Fatal(err)
	}
	bt, err := b.toolsFor(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if bt.kind != "playwright" || bt.tools[0].Name() != "mcp__playwright__navigate" {
		t.Fatalf("tools = %+v", bt)
	}
	if !slices.Contains(fc.cfgs[0].Args, "http://127.0.0.1:9333") {
		t.Fatalf("preset args = %v", fc.cfgs[0].Args)
	}
}

func TestBrowserToolsHonorTheRunOverride(t *testing.T) {
	b, _ := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	bt, err := b.toolsFor(context.Background(), "chrome-devtools")
	if err != nil || bt.kind != "chrome-devtools" {
		t.Fatalf("tools = %+v, err = %v", bt, err)
	}
}

func TestBrowserSessionIsReused(t *testing.T) {
	b, fc := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	first, _ := b.toolsFor(context.Background(), "")
	second, _ := b.toolsFor(context.Background(), "")
	if len(fc.cfgs) != 1 || first.key() != second.key() {
		t.Fatalf("connects = %d, keys %q %q", len(fc.cfgs), first.key(), second.key())
	}
}

func TestNewEndpointClosesOldSessions(t *testing.T) {
	b, fc := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	before, _ := b.toolsFor(context.Background(), "")
	_ = b.SetEndpoint("http://127.0.0.1:9444")
	after, _ := b.toolsFor(context.Background(), "")
	if !fc.sessions[0].closed {
		t.Error("old session left open")
	}
	if before.key() == after.key() {
		t.Errorf("key unchanged: %q", after.key())
	}
}

func TestClearingTheEndpointRemovesBrowserTools(t *testing.T) {
	b, _ := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	_ = b.SetEndpoint("")
	if bt, _ := b.toolsFor(context.Background(), ""); len(bt.tools) != 0 {
		t.Fatalf("tools = %+v", bt)
	}
}

func TestBrowserConnectFailureClearsTheEndpoint(t *testing.T) {
	b, fc := newBrowsers(t)
	fc.err = errors.New("ECONNREFUSED")
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	_, err := b.toolsFor(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "browser panel") {
		t.Fatalf("err = %v", err)
	}
	if b.Status().Connected {
		t.Fatal("endpoint kept after a failed connect")
	}
}

func TestSetEndpointRejectsNonLoopback(t *testing.T) {
	b, _ := newBrowsers(t)
	if err := b.SetEndpoint("http://example.com:9333"); err == nil {
		t.Fatal("accepted a remote endpoint")
	}
}

func TestBrowserCloseRemovesPlaywrightOutput(t *testing.T) {
	b, fc := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	_, _ = b.toolsFor(context.Background(), "")
	args := fc.cfgs[0].Args
	dir := args[slices.Index(args, "--output-dir")+1]
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("output dir: %v", err)
	}
	_ = b.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("output dir left behind: %v", err)
	}
}

func TestBrowserStatus(t *testing.T) {
	b, _ := newBrowsers(t)
	_ = b.SetEndpoint("http://127.0.0.1:9333")
	st := b.Status()
	if !st.Connected || st.Default != "playwright" || !slices.Equal(st.Kinds, []string{"playwright", "chrome-devtools"}) {
		t.Fatalf("status = %+v", st)
	}
}

func TestServiceRunGetsBrowserToolsAndTracesTheMCP(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "n", Name: "mcp__chrome-devtools__navigate", Args: map[string]any{}}}},
		llm.NewAssistantMessage("done"),
	)
	fc := &fakeConnector{}
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: model, NoVerifyGate: true, ConnectMCP: fc.connect})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.SetBrowserEndpoint("http://127.0.0.1:9333"); err != nil {
		t.Fatal(err)
	}
	req := userReq("t1", t.TempDir(), "open it")
	req.BrowserMCP = "chrome-devtools"
	events, err := collect(t, svc, req)
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *llm.Message
	for _, m := range messages(events) {
		if m.ToolCallID == "n" {
			toolMsg = &m
		}
	}
	if toolMsg == nil || toolMsg.Status == "error" {
		t.Fatalf("browser tool message = %+v", toolMsg)
	}
	tr, err := svc.GetTrace(context.Background(), "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got := tr.LLMCalls[0].BrowserMCP; got != "chrome-devtools" {
		t.Fatalf("trace browser_mcp = %q", got)
	}
}

func TestServiceRejectsAnUnknownBrowserMCP(t *testing.T) {
	svc, _ := newService(t)
	req := userReq("t1", t.TempDir(), "x")
	req.BrowserMCP = "selenium"
	if _, err := collect(t, svc, req); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("err = %v", err)
	}
}
