// Package sandbox creates remote sandbox backends by provider name
// (Python `integrations/sandbox_*.py`).
package sandbox

import (
	"context"

	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/internal/notimpl"
)

// Config selects and configures a sandbox.
type Config struct {
	Provider string
	// ID reuses an existing sandbox instead of creating one.
	ID      string
	Setup   string
	Options map[string]any
}

// Provider creates and deletes sandboxes of one kind (langsmith, daytona, modal, runloop, ...).
type Provider interface {
	Name() string
	Create(ctx context.Context, cfg Config) (backend.SandboxBackend, error)
	Delete(ctx context.Context, id string) error
}

// Register adds a provider to the registry.
func Register(p Provider) error {
	return notimpl.Err("sandbox.Register")
}

// Open creates (or reattaches to) a sandbox using the configured provider.
func Open(ctx context.Context, cfg Config) (backend.SandboxBackend, error) {
	return nil, notimpl.Err("sandbox.Open")
}
