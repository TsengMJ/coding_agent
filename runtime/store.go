package runtime

import (
	"context"
	"time"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
)

// Namespace is a hierarchical store namespace, e.g. {"user-1", "filesystem"}.
type Namespace []string

// Item is one stored value (LangGraph `Item`).
type Item struct {
	Namespace Namespace
	Key       string
	Value     map[string]any
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SearchOptions filters Store.Search.
type SearchOptions struct {
	Filter map[string]any
	Query  string
	Limit  int
	Offset int
}

// Store is a cross-thread key-value store (LangGraph `BaseStore`).
type Store interface {
	Get(ctx context.Context, ns Namespace, key string) (*Item, error)
	Put(ctx context.Context, ns Namespace, key string, value map[string]any) error
	Delete(ctx context.Context, ns Namespace, key string) error
	Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error)
}

// InMemoryStore keeps items in process memory (LangGraph `InMemoryStore`).
type InMemoryStore struct{}

// NewInMemoryStore returns an empty in-memory store.
func NewInMemoryStore() *InMemoryStore { return &InMemoryStore{} }

func (s *InMemoryStore) Get(ctx context.Context, ns Namespace, key string) (*Item, error) {
	return nil, notimpl.Err("runtime.InMemoryStore.Get")
}

func (s *InMemoryStore) Put(ctx context.Context, ns Namespace, key string, value map[string]any) error {
	return notimpl.Err("runtime.InMemoryStore.Put")
}

func (s *InMemoryStore) Delete(ctx context.Context, ns Namespace, key string) error {
	return notimpl.Err("runtime.InMemoryStore.Delete")
}

func (s *InMemoryStore) Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error) {
	return nil, notimpl.Err("runtime.InMemoryStore.Search")
}
