// Package browser holds the MCP servers that drive the desktop app's
// browser panel through its filtered DevTools endpoint.
package browser

import (
	"context"
	"fmt"
	"maps"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// Kind names a browser MCP server.
type Kind string

const (
	Playwright     Kind = "playwright"
	ChromeDevTools Kind = "chrome-devtools"
)

// Kinds lists the supported browser MCP servers.
var Kinds = []Kind{Playwright, ChromeDevTools}

// Pinned so runs stay comparable; bump deliberately.
const (
	PlaywrightMCPVersion     = "0.0.83"
	ChromeDevToolsMCPVersion = "1.10.1"
)

// ParseKind parses a browser.mcp value; empty means Playwright.
func ParseKind(s string) (Kind, error) {
	switch Kind(s) {
	case "":
		return Playwright, nil
	case Playwright, ChromeDevTools:
		return Kind(s), nil
	}
	return "", fmt.Errorf("browser mcp must be %q or %q, got %q", Playwright, ChromeDevTools, s)
}

// Preset is the MCP server for kind connected to the DevTools endpoint
// cdpURL. Both run in outputDir, which also receives Playwright's
// snapshots and logs; Playwright confines named files to it.
func Preset(kind Kind, cdpURL, outputDir string) (mcp.ServerConfig, error) {
	cfg := mcp.ServerConfig{Name: string(kind), Transport: mcp.TransportStdio, Command: "npx", Dir: outputDir}
	switch kind {
	case Playwright:
		cfg.Args = []string{"-y", "@playwright/mcp@" + PlaywrightMCPVersion, "--cdp-endpoint", cdpURL, "--output-dir", outputDir, "--no-webmcp"}
	case ChromeDevTools:
		cfg.Args = []string{"-y", "chrome-devtools-mcp@" + ChromeDevToolsMCPVersion, "--browserUrl", cdpURL, "--no-usage-statistics"}
	default:
		return mcp.ServerConfig{}, fmt.Errorf("unknown browser mcp %q", kind)
	}
	return cfg, nil
}

// ContainerChromium is where the workspace image installs Chromium.
const ContainerChromium = "/usr/bin/chromium"

// ScreenshotTool and ClickTool are the Playwright MCP tools the panel of a
// workspace browser uses for frames and the user's clicks.
const (
	ScreenshotTool = "browser_take_screenshot"
	ClickTool      = "browser_mouse_click_xy"
)

// ContainerPreset is Playwright MCP running in a workspace container
// through `docker exec -i`, with its own headless Chromium. The browser
// and its DevTools port stay inside the container; only MCP crosses it.
// The vision tools let the panel forward clicks at coordinates.
func ContainerPreset(dockerBinary, container string) mcp.ServerConfig {
	return mcp.ServerConfig{Name: string(Playwright), Transport: mcp.TransportStdio, Command: dockerBinary,
		Args: []string{"exec", "-i", container,
			"npx", "-y", "@playwright/mcp@" + PlaywrightMCPVersion,
			"--headless", "--isolated", "--no-sandbox", "--browser", "chromium",
			"--executable-path", ContainerChromium, "--caps", "vision",
			"--output-dir", "/tmp/quill-browser", "--no-webmcp"}}
}

// CheckEndpoint accepts only a bare http://<loopback>:<port> URL: the
// endpoint controls a browser, so it must never point off the machine.
func CheckEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("browser endpoint: %w", err)
	}
	if u.Scheme != "http" || u.User != nil || u.Port() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" {
		return fmt.Errorf("browser endpoint must look like http://127.0.0.1:<port>, got %q", raw)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("browser endpoint must be on loopback, got %q", raw)
	}
	return nil
}

// policy keeps a browser MCP server inside the panel: denied tools run
// code or read files outside it. Hidden arguments map to the reason they
// are refused.
type policy struct {
	deny   []string
	hidden map[string]string
}

const (
	writesFiles = "quill keeps browser tools from writing files; omit it to get the result inline"
	oneContext  = "the browser panel has a single browser context; omit it"
)

var policies = map[Kind]policy{
	Playwright: {deny: []string{"browser_run_code_unsafe"}},
	ChromeDevTools: {deny: []string{"upload_file"}, hidden: map[string]string{
		"filePath": writesFiles, "requestFilePath": writesFiles, "responseFilePath": writesFiles, "outputDirPath": writesFiles,
		"isolatedContext": oneContext,
	}},
}

// Restrict applies kind's policy to the tools of its MCP server.
func Restrict(kind Kind, tools []tool.Tool) []tool.Tool {
	p := policies[kind]
	prefix := mcp.ToolPrefix + string(kind) + "__"
	var out []tool.Tool
	for _, t := range tools {
		if slices.Contains(p.deny, strings.TrimPrefix(t.Name(), prefix)) {
			continue
		}
		if hidden := presentArgs(t.Schema(), p.hidden); len(hidden) > 0 {
			t = &hiddenArgs{Tool: t, hidden: hidden, reasons: p.hidden}
		}
		out = append(out, t)
	}
	return out
}

func presentArgs(s tool.Schema, names map[string]string) []string {
	props, _ := s["properties"].(map[string]any)
	var out []string
	for n := range names {
		if _, ok := props[n]; ok {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

type hiddenArgs struct {
	tool.Tool
	hidden  []string
	reasons map[string]string
}

func (t *hiddenArgs) Schema() tool.Schema {
	s := maps.Clone(t.Tool.Schema())
	props := maps.Clone(s["properties"].(map[string]any))
	for _, n := range t.hidden {
		delete(props, n)
	}
	s["properties"] = props
	if req, ok := s["required"].([]any); ok {
		s["required"] = slices.DeleteFunc(slices.Clone(req), func(v any) bool {
			name, _ := v.(string)
			return slices.Contains(t.hidden, name)
		})
	}
	return s
}

func (t *hiddenArgs) Invoke(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
	for _, n := range t.hidden {
		if _, ok := args[n]; ok {
			msg := fmt.Sprintf("%s is not allowed: %s", n, t.reasons[n])
			return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(msg)}}, nil
		}
	}
	return t.Tool.Invoke(ctx, rt, args)
}
