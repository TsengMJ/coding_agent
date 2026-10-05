// Package extensions loads server-side extensions contributing middleware,
// tools and virtual storage routes (Python `extensions/*`, see EXTENSIONS.md).
//
// Python loads arbitrary modules; the Go port registers extensions at compile
// time through Register.
package extensions

import (
	"github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/internal/notimpl"
	"github.com/tsengmingchun/quill/tool"
)

// Extension contributes capabilities to the agent server.
type Extension interface {
	Name() string
	Middleware() []agent.Middleware
	Tools() []tool.Tool
	// Routes maps path prefixes to backends mounted in the CompositeBackend.
	Routes() map[string]backend.Backend
}

// Register adds an extension to the global registry.
func Register(e Extension) error {
	return notimpl.Err("extensions.Register")
}

// Loaded returns the registered extensions allowed by trust settings.
func Loaded() ([]Extension, error) {
	return nil, notimpl.Err("extensions.Loaded")
}
