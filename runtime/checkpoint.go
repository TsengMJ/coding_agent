package runtime

import (
	"context"
	"time"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// Checkpoint is a persisted snapshot of one thread's state.
type Checkpoint struct {
	ID        string
	ThreadID  string
	ParentID  string
	State     State
	CreatedAt time.Time
	Metadata  map[string]any
	// PendingInterrupts are interrupts awaiting a resume value.
	PendingInterrupts []Interrupt
}

// Checkpointer persists thread state between runs (LangGraph `Checkpointer`).
type Checkpointer interface {
	// Latest returns the newest checkpoint for a thread, or nil if none exists.
	Latest(ctx context.Context, threadID string) (*Checkpoint, error)
	Put(ctx context.Context, cp Checkpoint) error
	List(ctx context.Context, threadID string, limit int) ([]Checkpoint, error)
	DeleteThread(ctx context.Context, threadID string) error
}

// MemoryCheckpointer keeps checkpoints in process memory (LangGraph `InMemorySaver`).
type MemoryCheckpointer struct{}

// NewMemoryCheckpointer returns an empty in-memory checkpointer.
func NewMemoryCheckpointer() *MemoryCheckpointer { return &MemoryCheckpointer{} }

func (c *MemoryCheckpointer) Latest(ctx context.Context, threadID string) (*Checkpoint, error) {
	return nil, notimpl.Err("runtime.MemoryCheckpointer.Latest")
}

func (c *MemoryCheckpointer) Put(ctx context.Context, cp Checkpoint) error {
	return notimpl.Err("runtime.MemoryCheckpointer.Put")
}

func (c *MemoryCheckpointer) List(ctx context.Context, threadID string, limit int) ([]Checkpoint, error) {
	return nil, notimpl.Err("runtime.MemoryCheckpointer.List")
}

func (c *MemoryCheckpointer) DeleteThread(ctx context.Context, threadID string) error {
	return notimpl.Err("runtime.MemoryCheckpointer.DeleteThread")
}
