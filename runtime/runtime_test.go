package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

var (
	_ Checkpointer = (*MemoryCheckpointer)(nil)
	_ Store        = (*InMemoryStore)(nil)
	_ error        = (*InterruptError)(nil)
)

func TestMemoryCheckpointerNotImplemented(t *testing.T) {
	_, err := NewMemoryCheckpointer().Latest(context.Background(), "t1")
	if !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}
