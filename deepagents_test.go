package deepagents

import (
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/middleware/asyncsubagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/filesystem"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/memory"
	"github.com/tsengmingchun/deepagents_golang/middleware/patchtoolcalls"
	"github.com/tsengmingchun/deepagents_golang/middleware/promptcaching"
	"github.com/tsengmingchun/deepagents_golang/middleware/rubric"
	"github.com/tsengmingchun/deepagents_golang/middleware/skills"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	"github.com/tsengmingchun/deepagents_golang/middleware/toolexclusion"
	"github.com/tsengmingchun/deepagents_golang/middleware/unsupportedcontent"
)

// Hook conformance: each middleware exposes the hooks its Python counterpart overrides.
var (
	_ agent.ToolProvider        = (*filesystem.Middleware)(nil)
	_ agent.StateSchemaProvider = (*filesystem.Middleware)(nil)
	_ agent.WrapModelCallHook   = (*filesystem.Middleware)(nil)
	_ agent.WrapToolCallHook    = (*filesystem.Middleware)(nil)

	_ agent.ToolProvider      = (*subagents.Middleware)(nil)
	_ agent.WrapModelCallHook = (*subagents.Middleware)(nil)

	_ agent.ToolProvider        = (*asyncsubagents.Middleware)(nil)
	_ agent.StateSchemaProvider = (*asyncsubagents.Middleware)(nil)

	_ agent.WrapModelCallHook   = (*summarization.Middleware)(nil)
	_ agent.StateSchemaProvider = (*summarization.Middleware)(nil)
	_ agent.ToolProvider        = (*summarization.ToolMiddleware)(nil)

	_ agent.BeforeAgentHook   = (*memory.Middleware)(nil)
	_ agent.WrapModelCallHook = (*memory.Middleware)(nil)

	_ agent.BeforeAgentHook   = (*skills.Middleware)(nil)
	_ agent.WrapModelCallHook = (*skills.Middleware)(nil)

	_ agent.BeforeAgentHook = (*rubric.Middleware)(nil)
	_ agent.AfterAgentHook  = (*rubric.Middleware)(nil)

	_ agent.BeforeAgentHook   = (*patchtoolcalls.Middleware)(nil)
	_ agent.WrapModelCallHook = (*unsupportedcontent.Middleware)(nil)
	_ agent.AfterModelHook    = (*hitl.Middleware)(nil)
	_ agent.ToolProvider      = (*todolist.Middleware)(nil)
	_ agent.WrapModelCallHook = (*promptcaching.Middleware)(nil)
	_ agent.WrapModelCallHook = (*toolexclusion.Middleware)(nil)

	_ subagents.Spec = subagents.SubAgent{}
	_ subagents.Spec = subagents.CompiledSubAgent{}
	_ subagents.Spec = asyncsubagents.AsyncSubAgent{}
)

func TestCreateDeepAgentNotImplemented(t *testing.T) {
	if _, err := CreateDeepAgent(WithModelSpec("openai:gpt-5.5")); !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}

func TestRequiredMiddlewareNames(t *testing.T) {
	want := map[string]bool{"FilesystemMiddleware": true, "SubAgentMiddleware": true}
	for _, name := range RequiredMiddleware {
		if !want[name] {
			t.Fatalf("unexpected required middleware %q", name)
		}
	}
}

func TestFilesystemExposesAllToolsByDefault(t *testing.T) {
	if got := len(filesystem.New(filesystem.Options{}).Tools()); got != len(filesystem.AllTools) {
		t.Fatalf("want %d tools, got %d", len(filesystem.AllTools), got)
	}
}
