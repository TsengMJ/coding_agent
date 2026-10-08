package mcp

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const testServerEnv = "QUILL_MCP_TEST_SERVER"

// TestMain lets the test binary double as a stdio MCP server, so the stdio
// tests start a real child process.
func TestMain(m *testing.M) {
	if os.Getenv(testServerEnv) == "1" {
		if err := newTestServer().Run(context.Background(), &sdk.StdioTransport{}); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var pngBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func newTestServer() *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "1"}, nil)
	object := map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
	s.AddTool(&sdk.Tool{Name: "echo", Description: "Echo text back.", InputSchema: object},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "echo: " + string(req.Params.Arguments)}}}, nil
		})
	s.AddTool(&sdk.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "element not found"}}}, nil
		})
	s.AddTool(&sdk.Tool{Name: "screenshot", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{
				&sdk.TextContent{Text: "Took a screenshot"},
				&sdk.ImageContent{MIMEType: "image/png", Data: pngBytes},
			}}, nil
		})
	s.AddTool(&sdk.Tool{Name: "crash", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			os.Exit(3)
			return nil, nil
		})
	return s
}

func connectInMemory(t *testing.T, name string) Session {
	t.Helper()
	ctx := context.Background()
	clientT, serverT := sdk.NewInMemoryTransports()
	ss, err := newTestServer().Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	s, err := connectTransports(ctx, map[string]sdk.Transport{name: clientT})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func toolsByName(t *testing.T, s Session) map[string]tool.Tool {
	t.Helper()
	tools, err := s.Tools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]tool.Tool{}
	for _, tl := range tools {
		out[tl.Name()] = tl
	}
	return out
}

func invoke(t *testing.T, tl tool.Tool, args map[string]any) *tool.Result {
	t.Helper()
	res, err := tl.Invoke(context.Background(), &tool.Runtime{}, args)
	if err != nil {
		t.Fatalf("Invoke returned an error instead of an error result: %v", err)
	}
	return res
}

func TestToolsArePrefixedWithTheServerName(t *testing.T) {
	tools := toolsByName(t, connectInMemory(t, "browser"))
	echo, ok := tools["mcp__browser__echo"]
	if !ok {
		t.Fatalf("tools = %v, want mcp__browser__echo", tools)
	}
	if echo.Description() != "Echo text back." {
		t.Errorf("description = %q", echo.Description())
	}
	if echo.Schema()["type"] != "object" {
		t.Errorf("schema = %v", echo.Schema())
	}
}

func TestCallReturnsTextContent(t *testing.T) {
	tools := toolsByName(t, connectInMemory(t, "browser"))
	res := invoke(t, tools["mcp__browser__echo"], map[string]any{"text": "hi"})
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != `echo: {"text":"hi"}` {
		t.Fatalf("result = %+v", res)
	}
}

func TestToolErrorBecomesErrorResult(t *testing.T) {
	tools := toolsByName(t, connectInMemory(t, "browser"))
	res := invoke(t, tools["mcp__browser__fail"], nil)
	if !res.IsError || res.Content[0].Text != "element not found" {
		t.Fatalf("result = %+v", res)
	}
}

func TestImageContentBecomesImageBlock(t *testing.T) {
	tools := toolsByName(t, connectInMemory(t, "browser"))
	res := invoke(t, tools["mcp__browser__screenshot"], nil)
	if len(res.Content) != 2 {
		t.Fatalf("content = %+v", res.Content)
	}
	img := res.Content[1]
	if img.Type != llm.BlockImage || img.MimeType != "image/png" || img.Base64 != base64.StdEncoding.EncodeToString(pngBytes) {
		t.Errorf("image = %+v", img)
	}
}

func TestCallAfterCloseIsAnErrorResult(t *testing.T) {
	s := connectInMemory(t, "browser")
	tools := toolsByName(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	res := invoke(t, tools["mcp__browser__echo"], nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, `MCP server "browser"`) {
		t.Fatalf("result = %+v", res)
	}
}

func stdioConfig() ServerConfig {
	return ServerConfig{Name: "local", Transport: TransportStdio, Command: os.Args[0], Env: map[string]string{testServerEnv: "1"}}
}

func TestConnectStartsAStdioServer(t *testing.T) {
	s, err := Connect(context.Background(), []ServerConfig{stdioConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	res := invoke(t, toolsByName(t, s)["mcp__local__echo"], map[string]any{"text": "x"})
	if res.IsError || res.Content[0].Text != `echo: {"text":"x"}` {
		t.Fatalf("result = %+v", res)
	}
}

func TestCrashedServerGivesErrorResults(t *testing.T) {
	s, err := Connect(context.Background(), []ServerConfig{stdioConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tools := toolsByName(t, s)
	if res := invoke(t, tools["mcp__local__crash"], nil); !res.IsError {
		t.Fatalf("crash result = %+v", res)
	}
	if res := invoke(t, tools["mcp__local__echo"], nil); !res.IsError {
		t.Fatalf("call after crash = %+v", res)
	}
}

func TestConnectSkipsDisabledServers(t *testing.T) {
	cfg := stdioConfig()
	cfg.Disabled = true
	s, err := Connect(context.Background(), []ServerConfig{cfg})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if tools := toolsByName(t, s); len(tools) != 0 {
		t.Fatalf("tools = %v", tools)
	}
}

func TestConnectFailureNamesTheServer(t *testing.T) {
	cfg := ServerConfig{Name: "broken", Transport: TransportStdio, Command: filepath.Join(t.TempDir(), "missing")}
	_, err := Connect(context.Background(), []ServerConfig{cfg})
	if err == nil || !strings.Contains(err.Error(), `"broken"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectRejectsUnsupportedTransports(t *testing.T) {
	_, err := Connect(context.Background(), []ServerConfig{{Name: "remote", Transport: TransportHTTP, URL: "http://127.0.0.1:1"}})
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("err = %v", err)
	}
}

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigsReadsMCPServers(t *testing.T) {
	p := writeFile(t, t.TempDir(), "mcp.json", `{"mcpServers": {"pw": {"command": "npx", "args": ["@playwright/mcp"], "env": {"A": "1"}}}}`)
	cfgs, err := LoadConfigs(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 1 {
		t.Fatalf("cfgs = %+v", cfgs)
	}
	c := cfgs[0]
	if c.Name != "pw" || c.Transport != TransportStdio || c.Command != "npx" || c.Args[0] != "@playwright/mcp" || c.Env["A"] != "1" {
		t.Errorf("cfg = %+v", c)
	}
}

func TestLoadConfigsLaterFilesOverrideEarlierOnes(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.json", `{"mcpServers": {"x": {"command": "one"}, "y": {"command": "keep"}}}`)
	b := writeFile(t, dir, "b.json", `{"mcpServers": {"x": {"command": "two"}}}`)
	cfgs, err := LoadConfigs(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfgs) != 2 || cfgs[0].Name != "x" || cfgs[0].Command != "two" || cfgs[1].Command != "keep" {
		t.Fatalf("cfgs = %+v", cfgs)
	}
}

func TestLoadConfigsSkipsMissingFiles(t *testing.T) {
	cfgs, err := LoadConfigs(filepath.Join(t.TempDir(), "none.json"))
	if err != nil || len(cfgs) != 0 {
		t.Fatalf("cfgs = %+v, err = %v", cfgs, err)
	}
}

func TestLoadConfigsRejectsBadEntries(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":   `{`,
		"no command":     `{"mcpServers": {"x": {}}}`,
		"bad name":       `{"mcpServers": {"a b": {"command": "x"}}}`,
		"bad transport":  `{"mcpServers": {"x": {"transport": "carrier-pigeon", "command": "x"}}}`,
		"http needs url": `{"mcpServers": {"x": {"transport": "http"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			p := writeFile(t, t.TempDir(), "mcp.json", body)
			_, err := LoadConfigs(p)
			if err == nil || !strings.Contains(err.Error(), p) {
				t.Fatalf("err = %v, want an error naming %s", err, p)
			}
		})
	}
}

func TestIsToolName(t *testing.T) {
	if !IsToolName("mcp__pw__browser_click") || IsToolName("execute") {
		t.Fatal("IsToolName mismatch")
	}
}

func TestToolAllowed(t *testing.T) {
	allow := []string{"mcp__pw__browser_snapshot", "mcp__devtools__*"}
	for name, want := range map[string]bool{
		"mcp__pw__browser_snapshot": true,
		"mcp__pw__browser_click":    false,
		"mcp__devtools__click":      true,
	} {
		if got := ToolAllowed(name, allow); got != want {
			t.Errorf("ToolAllowed(%q) = %v, want %v", name, got, want)
		}
	}
}
