package browser

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func TestParseKind(t *testing.T) {
	for in, want := range map[string]Kind{"": Playwright, "playwright": Playwright, "chrome-devtools": ChromeDevTools} {
		got, err := ParseKind(in)
		if err != nil || got != want {
			t.Errorf("ParseKind(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseKind("selenium"); err == nil {
		t.Error("ParseKind accepted selenium")
	}
}

func TestPlaywrightPresetPinsTheVersionAndKeepsOutputOutOfTheProject(t *testing.T) {
	cfg, err := Preset(Playwright, "http://127.0.0.1:9333", "/tmp/out")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-y", "@playwright/mcp@" + PlaywrightMCPVersion, "--cdp-endpoint", "http://127.0.0.1:9333", "--output-dir", "/tmp/out", "--no-webmcp"}
	if cfg.Name != "playwright" || cfg.Command != "npx" || !slices.Equal(cfg.Args, want) {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestChromeDevToolsPresetPinsTheVersion(t *testing.T) {
	cfg, err := Preset(ChromeDevTools, "http://127.0.0.1:9333", "/tmp/out")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-y", "chrome-devtools-mcp@" + ChromeDevToolsMCPVersion, "--browserUrl", "http://127.0.0.1:9333", "--no-usage-statistics"}
	if cfg.Name != "chrome-devtools" || cfg.Command != "npx" || !slices.Equal(cfg.Args, want) {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestCheckEndpoint(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:9333", "http://localhost:1234", "http://[::1]:9"} {
		if err := CheckEndpoint(ok); err != nil {
			t.Errorf("CheckEndpoint(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ws://127.0.0.1:1", "http://example.com:9333", "http://10.0.0.1:9333", "http://127.0.0.1", "http://127.0.0.1:1/path", "http://u:p@127.0.0.1:1"} {
		if err := CheckEndpoint(bad); err == nil {
			t.Errorf("CheckEndpoint(%q) accepted", bad)
		}
	}
}

func fakeTool(name string, props ...string) tool.Tool {
	p := map[string]any{}
	for _, k := range props {
		p[k] = map[string]any{"type": "string"}
	}
	return &tool.Func{ToolName: name, ToolSchema: tool.Schema{"type": "object", "properties": p, "required": []any{"pageId", "filePath"}},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("ran")}}, nil
		}}
}

func names(tools []tool.Tool) []string {
	var out []string
	for _, t := range tools {
		out = append(out, t.Name())
	}
	return out
}

func TestRestrictDropsToolsThatEscapeThePanel(t *testing.T) {
	pw := Restrict(Playwright, []tool.Tool{fakeTool("mcp__playwright__browser_click"), fakeTool("mcp__playwright__browser_run_code_unsafe")})
	if got := names(pw); !slices.Equal(got, []string{"mcp__playwright__browser_click"}) {
		t.Errorf("playwright tools = %v", got)
	}
	cdt := Restrict(ChromeDevTools, []tool.Tool{fakeTool("mcp__chrome-devtools__click"), fakeTool("mcp__chrome-devtools__upload_file")})
	if got := names(cdt); !slices.Equal(got, []string{"mcp__chrome-devtools__click"}) {
		t.Errorf("chrome-devtools tools = %v", got)
	}
}

func TestRestrictHidesFilePathArguments(t *testing.T) {
	tl := Restrict(ChromeDevTools, []tool.Tool{fakeTool("mcp__chrome-devtools__take_screenshot", "pageId", "filePath")})[0]
	props := tl.Schema()["properties"].(map[string]any)
	if _, ok := props["filePath"]; ok {
		t.Errorf("schema still offers filePath: %v", props)
	}
	if _, ok := props["pageId"]; !ok {
		t.Errorf("schema lost pageId: %v", props)
	}
	if req := tl.Schema()["required"].([]any); !slices.Equal(req, []any{"pageId"}) {
		t.Errorf("required = %v", req)
	}
}

func TestRestrictRefusesCallsWithFilePaths(t *testing.T) {
	tl := Restrict(ChromeDevTools, []tool.Tool{fakeTool("mcp__chrome-devtools__get_network_request", "reqid", "responseFilePath")})[0]
	res, err := tl.Invoke(context.Background(), &tool.Runtime{}, map[string]any{"reqid": 1, "responseFilePath": "/Users/me/.zshrc"})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].Text, "responseFilePath") {
		t.Fatalf("result = %+v, err = %v", res, err)
	}
	res, _ = tl.Invoke(context.Background(), &tool.Runtime{}, map[string]any{"reqid": 1})
	if res.IsError || res.Content[0].Text != "ran" {
		t.Fatalf("plain call = %+v", res)
	}
}

func TestRestrictHidesIsolatedContexts(t *testing.T) {
	tl := Restrict(ChromeDevTools, []tool.Tool{fakeTool("mcp__chrome-devtools__new_page", "url", "isolatedContext")})[0]
	if _, ok := tl.Schema()["properties"].(map[string]any)["isolatedContext"]; ok {
		t.Errorf("schema still offers isolatedContext")
	}
}

func TestPresetsRunInTheOutputDir(t *testing.T) {
	for _, k := range Kinds {
		cfg, _ := Preset(k, "http://127.0.0.1:1", "/tmp/out")
		if cfg.Dir != "/tmp/out" {
			t.Errorf("%s dir = %q", k, cfg.Dir)
		}
	}
}
