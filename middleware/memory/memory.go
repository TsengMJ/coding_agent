// Package memory loads AGENTS.md-style memory files and injects them into
// the system prompt (Python `MemoryMiddleware`).
package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/runtime"
)

// ContentsStateKey holds loaded memory keyed by source path (private).
const ContentsStateKey = "memory_contents"

// Slot is the placeholder in SystemPrompt templates.
const Slot = "{agent_memory}"

// Options configures the middleware.
type Options struct {
	Backend backend.Backend
	// Sources are memory file paths loaded in order.
	Sources []string
	// AddCacheControl marks the memory block as an Anthropic cache breakpoint.
	AddCacheControl bool
	// SystemPrompt is the template; empty uses the default MEMORY_SYSTEM_PROMPT.
	// It must contain Slot.
	SystemPrompt string
	// Reload reads the sources again at the start of every invocation and
	// replaces the stored contents when they changed, so edits made since
	// the thread started (by the user or the agent) are seen.
	Reload bool
}

// Middleware loads memory once per thread (or per invocation with Reload)
// and injects it on every model call.
type Middleware struct {
	opts Options
}

// New returns a memory middleware.
func New(opts Options) (*Middleware, error) {
	if opts.Backend == nil {
		return nil, errors.New("memory.New: Backend is required")
	}
	if opts.SystemPrompt == "" {
		opts.SystemPrompt = SystemPrompt
	}
	if !strings.Contains(opts.SystemPrompt, Slot) {
		return nil, fmt.Errorf("memory.New: SystemPrompt must contain the %s slot", Slot)
	}
	return &Middleware{opts: opts}, nil
}

func (m *Middleware) Name() string { return "MemoryMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{ContentsStateKey: {Name: ContentsStateKey, Private: true}}
}

// BeforeAgent loads every source once per thread, or on every invocation
// with Reload. Missing files are skipped; other read errors fail the run.
func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, _ *agent.Runtime) (*runtime.Update, error) {
	stored, loaded := state.Values[ContentsStateKey]
	if loaded && !m.opts.Reload {
		return nil, nil
	}
	contents := map[string]string{}
	ctx = backend.WithRunContext(ctx, backend.NewRunContext(state))
	for i, res := range m.opts.Backend.DownloadFiles(ctx, m.opts.Sources) {
		path := m.opts.Sources[i]
		switch {
		case res.Error == backend.ErrFileNotFound:
		case res.Error != "":
			return nil, fmt.Errorf("load memory %s: %s", path, res.Error)
		default:
			contents[path] = string(res.Content)
		}
	}
	if loaded && maps.Equal(contentsFrom(stored), contents) {
		return nil, nil
	}
	return &runtime.Update{Values: map[string]any{ContentsStateKey: contents}}, nil
}

// WrapModelCall appends the formatted memory block to the system message.
func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	var contents map[string]string
	if req.State != nil {
		contents = contentsFrom(req.State.Values[ContentsStateKey])
	}
	updated := *req
	updated.SystemMessage = agent.AppendToSystemMessage(req.SystemMessage, m.format(contents))
	return next(ctx, &updated)
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

func (m *Middleware) format(contents map[string]string) string {
	var sections []string
	for _, path := range m.opts.Sources {
		body := strings.TrimRight(htmlComment.ReplaceAllString(contents[path], ""), " \t\r\n")
		if body != "" {
			sections = append(sections, path+"\n\n"+body)
		}
	}
	body := "(No memory loaded)"
	if len(sections) > 0 {
		body = strings.Join(sections, "\n\n")
	}
	return strings.Replace(m.opts.SystemPrompt, Slot, body, 1)
}

// contentsFrom accepts the in-memory map and its JSON checkpoint form.
func contentsFrom(v any) map[string]string {
	switch c := v.(type) {
	case map[string]string:
		return c
	case map[string]any:
		out := make(map[string]string, len(c))
		for k, val := range c {
			if s, ok := val.(string); ok {
				out[k] = s
			}
		}
		return out
	}
	return nil
}
