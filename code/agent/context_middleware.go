package agent

import (
	"context"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	rt "github.com/tsengmingchun/deepagents_golang/runtime"
)

// LiveContextSource marks the request-only message that carries the live
// project context.
const LiveContextSource = "live_context"

// liveContext adds the selected model and the project context to each
// request. A fixed project context goes into the system message. The live
// one (git status changes with every edit) goes into a message after the
// conversation, so the system message and history stay a byte-identical
// prefix that the provider can cache.
type liveContext struct {
	cwd     string
	model   llm.ChatModel
	project string
}

func (*liveContext) Name() string { return "LiveContextMiddleware" }

func (m *liveContext) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	section := modelIdentitySection(llm.ModelForContext(ctx, m.model)) + m.project
	live := ""
	if m.project == "" {
		live = LocalContext(ctx, m.cwd)
	}
	if section == "" && live == "" {
		return next(ctx, req)
	}
	updated := *req
	if section != "" {
		updated.SystemMessage = core.AppendToSystemMessage(req.SystemMessage, section)
	}
	if live != "" {
		tail := llm.NewUserMessage("<project-context>\nCurrent state of the working tree (refreshed every turn):\n\n" + live + "</project-context>")
		tail.Metadata = map[string]any{rt.SyntheticSourceKey: LiveContextSource}
		updated.Messages = append(req.Messages[:len(req.Messages):len(req.Messages)], tail)
	}
	return next(ctx, &updated)
}
