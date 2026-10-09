package server

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/tsengmingchun/quill/apps/agent/code/browser"
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// ErrNoBrowser means the thread's workspace has no browser running yet.
var ErrNoBrowser = errors.New("no browser is open in this thread yet; it starts with the first workspace run")

// Screenshot is GET /threads/{id}/browser/screenshot.
type Screenshot struct {
	MimeType string
	Data     []byte
}

// workspaceBrowsers keeps one Playwright MCP session per workspace, running
// inside its container (browser.ContainerPreset). The agent drives it with
// the browser tools; the desktop panel shows its screenshots and forwards
// clicks to it.
type workspaceBrowsers struct {
	connect func(context.Context, []mcp.ServerConfig) (mcp.Session, error)
	preset  func(workspaceID string) mcp.ServerConfig

	mu    sync.Mutex
	gen   int
	conns map[string]*wsBrowser
}

type wsBrowser struct {
	gen int
	// done closes once the connection attempt finished; err, session,
	// tools and byName are set before.
	done    chan struct{}
	err     error
	session mcp.Session
	tools   []tool.Tool
	// byName holds the unrestricted tools by Playwright tool name.
	byName map[string]tool.Tool
}

func newWorkspaceBrowsers(connect func(context.Context, []mcp.ServerConfig) (mcp.Session, error), preset func(string) mcp.ServerConfig) *workspaceBrowsers {
	return &workspaceBrowsers{connect: connect, preset: preset, conns: map[string]*wsBrowser{}}
}

// toolsFor connects the workspace's browser on first use. Workspaces
// connect independently, so a slow first `npx` download blocks only its
// own runs.
func (b *workspaceBrowsers) toolsFor(ctx context.Context, workspaceID string) (browserTools, error) {
	b.mu.Lock()
	c, ok := b.conns[workspaceID]
	if !ok {
		b.gen++
		c = &wsBrowser{gen: b.gen, done: make(chan struct{})}
		b.conns[workspaceID] = c
	}
	b.mu.Unlock()
	if !ok {
		c.err = c.open(ctx, b.connect, b.preset(workspaceID))
		close(c.done)
	}
	select {
	case <-c.done:
	case <-ctx.Done():
		return browserTools{}, ctx.Err()
	}
	if c.err != nil {
		b.mu.Lock()
		if b.conns[workspaceID] == c {
			delete(b.conns, workspaceID)
		}
		b.mu.Unlock()
		return browserTools{}, c.err
	}
	return browserTools{kind: browser.Playwright, gen: c.gen, tools: c.tools}, nil
}

func (c *wsBrowser) open(ctx context.Context, connect func(context.Context, []mcp.ServerConfig) (mcp.Session, error), cfg mcp.ServerConfig) error {
	s, err := connect(ctx, []mcp.ServerConfig{cfg})
	if err != nil {
		return fmt.Errorf("start the workspace browser: %w", err)
	}
	tools, err := s.Tools(ctx)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("list the workspace browser tools: %w", err)
	}
	prefix := mcp.ToolPrefix + cfg.Name + "__"
	c.byName = map[string]tool.Tool{}
	for _, t := range tools {
		c.byName[strings.TrimPrefix(t.Name(), prefix)] = t
	}
	c.session, c.tools = s, browser.Restrict(browser.Playwright, tools)
	return nil
}

// ready returns the workspace's connected browser, or nil.
func (b *workspaceBrowsers) ready(workspaceID string) *wsBrowser {
	b.mu.Lock()
	c := b.conns[workspaceID]
	b.mu.Unlock()
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
		if c.err == nil {
			return c
		}
	default:
	}
	return nil
}

// drop closes the workspace's browser; its container stopped.
func (b *workspaceBrowsers) drop(workspaceID string) {
	b.mu.Lock()
	c := b.conns[workspaceID]
	delete(b.conns, workspaceID)
	b.mu.Unlock()
	if c != nil {
		go c.close()
	}
}

func (c *wsBrowser) close() {
	<-c.done
	if c.session != nil {
		_ = c.session.Close()
	}
}

func (b *workspaceBrowsers) Close() {
	b.mu.Lock()
	conns := b.conns
	b.conns = map[string]*wsBrowser{}
	b.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// call runs one of the panel's Playwright tools.
func (c *wsBrowser) call(ctx context.Context, name string, args map[string]any) (*tool.Result, error) {
	t, ok := c.byName[name]
	if !ok {
		return nil, fmt.Errorf("the workspace browser has no %s tool", name)
	}
	res, err := t.Invoke(ctx, &tool.Runtime{}, args)
	if err != nil {
		return nil, err
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: %s", name, resultText(res))
	}
	return res, nil
}

func resultText(res *tool.Result) string {
	var parts []string
	for _, b := range res.Content {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// workspaceBrowserFor returns the browser of the thread's workspace.
func (s *Service) workspaceBrowserFor(ctx context.Context, threadID string) (*wsBrowser, error) {
	if err := s.AuthorizeThread(ctx, threadID); err != nil {
		return nil, err
	}
	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrThreadNotFound
	}
	if info.WorkspaceID == "" || s.wsBrowsers == nil {
		return nil, fmt.Errorf("%w: thread %s has no workspace browser", ErrBadRequest, threadID)
	}
	c := s.wsBrowsers.ready(info.WorkspaceID)
	if c == nil {
		return nil, ErrNoBrowser
	}
	return c, nil
}

// BrowserScreenshot is the current page of the thread's workspace browser,
// for the desktop panel.
func (s *Service) BrowserScreenshot(ctx context.Context, threadID string) (*Screenshot, error) {
	c, err := s.workspaceBrowserFor(ctx, threadID)
	if err != nil {
		return nil, err
	}
	res, err := c.call(ctx, browser.ScreenshotTool, map[string]any{"type": "jpeg"})
	if err != nil {
		return nil, err
	}
	for _, b := range res.Content {
		if b.Type == llm.BlockImage && b.Base64 != "" {
			data, err := base64.StdEncoding.DecodeString(b.Base64)
			if err != nil {
				return nil, fmt.Errorf("decode screenshot: %w", err)
			}
			return &Screenshot{MimeType: b.MimeType, Data: data}, nil
		}
	}
	return nil, errors.New("the browser returned no screenshot")
}

// BrowserClick clicks the thread's workspace browser at page coordinates
// (CSS pixels of the screenshot) for the user.
func (s *Service) BrowserClick(ctx context.Context, threadID string, x, y float64) error {
	c, err := s.workspaceBrowserFor(ctx, threadID)
	if err != nil {
		return err
	}
	_, err = c.call(ctx, browser.ClickTool, map[string]any{"element": "the user's click in the quill panel", "x": x, "y": y})
	return err
}

// browserToolsFor picks a run's browser: the workspace's own, else the
// desktop panel. A workspace browser that fails to start leaves the run
// without browser tools instead of failing it.
func (s *Service) browserToolsFor(ctx context.Context, req RunRequest) (browserTools, error) {
	if req.WorkspaceID == "" {
		return s.browsers.toolsFor(ctx, req.BrowserMCP)
	}
	if s.wsBrowsers == nil {
		return browserTools{}, nil
	}
	bt, err := s.wsBrowsers.toolsFor(ctx, req.WorkspaceID)
	if err != nil {
		if ctx.Err() != nil {
			return browserTools{}, ctx.Err()
		}
		slog.WarnContext(ctx, "workspace browser unavailable", "workspace", req.WorkspaceID, "err", err)
		return browserTools{}, nil
	}
	return bt, nil
}

// dropWorkspaceBrowser forgets a stopped workspace's browser and the agents
// built on it.
func (s *Service) dropWorkspaceBrowser(workspaceID string) {
	s.wsBrowsers.drop(workspaceID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.agents {
		if k.workspace == workspaceID && k.browser != "" {
			delete(s.agents, k)
		}
	}
}
