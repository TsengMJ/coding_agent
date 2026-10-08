// Package unsupportedcontent rewrites multimodal blocks into a shape the
// model accepts (Python `UnsupportedContentMiddleware`). Images in tool
// results move to a user message right after the tool results when the
// model only accepts images from the user.
package unsupportedcontent

import (
	"context"
	"fmt"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// Middleware filters request messages against the model profile. It must
// run after summarization, whose cutoff indexes the unmodified history.
type Middleware struct{}

// New returns the middleware.
func New() *Middleware { return &Middleware{} }

func (m *Middleware) Name() string { return "UnsupportedContentMiddleware" }

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	if llm.ModelForContext(ctx, req.Model).Profile().ToolMessageImageInputs || !hasToolImages(req.Messages) {
		return next(ctx, req)
	}
	r := *req
	r.Messages = moveToolImages(req.Messages)
	return next(ctx, &r)
}

func hasToolImages(msgs []llm.Message) bool {
	for _, msg := range msgs {
		if msg.Role == llm.RoleTool && countImages(msg) > 0 {
			return true
		}
	}
	return false
}

func countImages(msg llm.Message) int {
	n := 0
	for _, c := range msg.Content {
		if c.Type == llm.BlockImage {
			n++
		}
	}
	return n
}

// moveToolImages keeps each run of tool results together (providers
// require them right after the assistant's tool calls) and follows the
// run with one user message holding its images.
func moveToolImages(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(msgs)+1)
	var moved []llm.ContentBlock
	flush := func() {
		if len(moved) > 0 {
			out = append(out, llm.Message{Role: llm.RoleUser, Content: moved})
			moved = nil
		}
	}
	for _, msg := range msgs {
		if msg.Role != llm.RoleTool {
			flush()
			out = append(out, msg)
			continue
		}
		n := countImages(msg)
		if n == 0 {
			out = append(out, msg)
			continue
		}
		label := fmt.Sprintf("Image output of the %s tool call (%s):", msg.Name, msg.ToolCallID)
		moved = append(moved, llm.TextBlock(label))
		content := make([]llm.ContentBlock, 0, len(msg.Content))
		for _, c := range msg.Content {
			if c.Type == llm.BlockImage {
				moved = append(moved, c)
				continue
			}
			content = append(content, c)
		}
		content = append(content, llm.TextBlock(fmt.Sprintf("[%d image(s) attached in the next user message]", n)))
		msg.Content = content
		out = append(out, msg)
	}
	flush()
	return out
}
