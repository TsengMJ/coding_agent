package agent

import (
	"context"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
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
		tail := requestTail(LiveContextSource, "<project-context>\nCurrent state of the working tree (refreshed every turn):\n\n"+live+"</project-context>")
		updated.Messages = append(req.Messages[:len(req.Messages):len(req.Messages)], tail)
	}
	return next(ctx, &updated)
}

// requestTail builds a message that follows the conversation in one request
// only and is replaced in the next. It must be a system message: OpenAI
// writes the prompt cache at the end of the latest user message, so a
// trailing user message that changes every request leaves nothing of the
// shared prefix to reuse, and it also re-renders the tool-call history
// before it (measured: ~17k more input tokens, cache hits ~35% instead of
// ~87%).
func requestTail(source, text string) llm.Message {
	m := llm.NewSystemMessage(text)
	m.Metadata = map[string]any{rt.SyntheticSourceKey: source}
	return m
}
