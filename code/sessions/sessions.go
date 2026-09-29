// Package sessions manages persisted conversation threads backed by a
// SQLite checkpointer (Python `sessions.py`).
package sessions

import (
	"context"
	"time"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// ThreadInfo summarizes one thread for listing and resume.
type ThreadInfo struct {
	ThreadID      string
	AgentName     string
	Cwd           string
	UpdatedAt     time.Time
	MessageCount  int
	InitialPrompt string
}

// ListOptions filters thread listings.
type ListOptions struct {
	AgentName string
	Limit     int
}

// Manager is the thread catalog.
type Manager interface {
	List(ctx context.Context, opts ListOptions) ([]ThreadInfo, error)
	MostRecent(ctx context.Context, agentName string) (*ThreadInfo, error)
	Exists(ctx context.Context, threadID string) (bool, error)
	FindSimilar(ctx context.Context, threadID string, limit int) ([]string, error)
	Delete(ctx context.Context, threadID string) error
	SetMetadata(ctx context.Context, threadID, agentName, cwd string) error
	Checkpointer() runtime.Checkpointer
}

// Open opens (creating if needed) the sessions database at dbPath.
func Open(ctx context.Context, dbPath string) (Manager, error) {
	return nil, notimpl.Err("sessions.Open")
}

// NewThreadID returns a fresh thread identifier.
func NewThreadID() string { return "" }
