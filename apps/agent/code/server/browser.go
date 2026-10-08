package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"

	"github.com/tsengmingchun/quill/apps/agent/code/browser"
	"github.com/tsengmingchun/quill/apps/agent/code/mcp"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// BrowserStatus answers GET /browser.
type BrowserStatus struct {
	// Connected reports whether the desktop app registered its panel.
	Connected bool `json:"connected"`
	// Default is the browser MCP runs use unless they pick another.
	Default string   `json:"default"`
	Kinds   []string `json:"kinds"`
}

// browserManager connects browser MCP servers to the panel endpoint the
// desktop app registers (PUT /browser), one session per kind, started on
// first use.
type browserManager struct {
	defaultKind browser.Kind
	connect     func(context.Context, []mcp.ServerConfig) (mcp.Session, error)

	mu       sync.Mutex
	endpoint string
	// gen changes with every endpoint, so cached agents built on the old
	// sessions are not reused.
	gen   int
	conns map[browser.Kind]*browserConn
}

type browserConn struct {
	session mcp.Session
	tools   []tool.Tool
	dir     string
}

// browserTools are the tools a run gets from the panel; kind is empty when
// no panel is registered.
type browserTools struct {
	kind  browser.Kind
	gen   int
	tools []tool.Tool
}

// key identifies the tool set for the agent cache.
func (b browserTools) key() string {
	if b.kind == "" {
		return ""
	}
	return string(b.kind) + "@" + strconv.Itoa(b.gen)
}

func newBrowserManager(defaultKind browser.Kind, connect func(context.Context, []mcp.ServerConfig) (mcp.Session, error)) *browserManager {
	return &browserManager{defaultKind: defaultKind, connect: connect, conns: map[browser.Kind]*browserConn{}}
}

// SetEndpoint registers the panel's DevTools endpoint; empty unregisters it.
func (b *browserManager) SetEndpoint(cdpURL string) error {
	if cdpURL != "" {
		if err := browser.CheckEndpoint(cdpURL); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if cdpURL == b.endpoint {
		return nil
	}
	if err := b.closeConnsLocked(); err != nil {
		log.Printf("quill: browser panel: %v", err)
	}
	b.endpoint = cdpURL
	b.gen++
	return nil
}

func (b *browserManager) Status() BrowserStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	kinds := make([]string, len(browser.Kinds))
	for i, k := range browser.Kinds {
		kinds[i] = string(k)
	}
	return BrowserStatus{Connected: b.endpoint != "", Default: string(b.defaultKind), Kinds: kinds}
}

// toolsFor returns the panel tools of kind (empty: the default). A panel
// that cannot be reached is unregistered, so the next run goes on without
// it instead of failing again.
func (b *browserManager) toolsFor(ctx context.Context, kind string) (browserTools, error) {
	k := b.defaultKind
	if kind != "" {
		var err error
		if k, err = browser.ParseKind(kind); err != nil {
			return browserTools{}, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.endpoint == "" {
		return browserTools{}, nil
	}
	if c, ok := b.conns[k]; ok {
		return browserTools{kind: k, gen: b.gen, tools: c.tools}, nil
	}
	c, err := b.connectLocked(ctx, k)
	if err != nil {
		_ = b.closeConnsLocked()
		b.endpoint = ""
		b.gen++
		return browserTools{}, fmt.Errorf("cannot reach the desktop browser panel with %s MCP (reopen the desktop app to retry): %w", k, err)
	}
	b.conns[k] = c
	return browserTools{kind: k, gen: b.gen, tools: c.tools}, nil
}

func (b *browserManager) connectLocked(ctx context.Context, k browser.Kind) (*browserConn, error) {
	dir, err := os.MkdirTemp("", "quill-browser-"+string(k)+"-")
	if err != nil {
		return nil, err
	}
	cfg, err := browser.Preset(k, b.endpoint, dir)
	if err == nil {
		var s mcp.Session
		if s, err = b.connect(ctx, []mcp.ServerConfig{cfg}); err == nil {
			var tools []tool.Tool
			if tools, err = s.Tools(ctx); err == nil {
				return &browserConn{session: s, tools: browser.Restrict(k, tools), dir: dir}, nil
			}
			_ = s.Close()
		}
	}
	_ = os.RemoveAll(dir)
	return nil, err
}

func (b *browserManager) closeConnsLocked() error {
	var errs []error
	for k, c := range b.conns {
		if err := c.session.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s MCP: %w", k, err))
		}
		_ = os.RemoveAll(c.dir)
		delete(b.conns, k)
	}
	return errors.Join(errs...)
}

func (b *browserManager) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closeConnsLocked()
}
