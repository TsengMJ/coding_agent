// Package unsupportedcontent replaces multimodal blocks the model cannot
// accept with text placeholders (Python `UnsupportedContentMiddleware`).
package unsupportedcontent

import (
	"context"

	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/internal/notimpl"
)

// Middleware filters request messages against the model profile.
type Middleware struct{}

// New returns the middleware.
func New() *Middleware { return &Middleware{} }

func (m *Middleware) Name() string { return "UnsupportedContentMiddleware" }

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("unsupportedcontent.Middleware.WrapModelCall")
}
