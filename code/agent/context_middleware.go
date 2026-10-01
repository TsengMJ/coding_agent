package agent

import (
	"context"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// liveContext adds the selected model and current working tree to each request.
type liveContext struct {
	cwd     string
	model   llm.ChatModel
	project string
}

func (*liveContext) Name() string { return "LiveContextMiddleware" }

func (m *liveContext) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	section := modelIdentitySection(llm.ModelForContext(ctx, m.model))
	project := m.project
	if project == "" {
		project = LocalContext(ctx, m.cwd)
	}
	section += project
	if section == "" {
		return next(ctx, req)
	}
	updated := *req
	updated.SystemMessage = core.AppendToSystemMessage(req.SystemMessage, section)
	return next(ctx, &updated)
}
