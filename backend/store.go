package backend

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// NamespaceFactory derives the store namespace for a run (e.g. per user).
type NamespaceFactory func(ctx context.Context, rc *RunContext) (runtime.Namespace, error)

// StoreBackend persists files in a runtime.Store, durable across threads.
type StoreBackend struct {
	unimplemented
	namespace NamespaceFactory
	// store overrides the run's store when non-nil.
	store runtime.Store
}

// NewStoreBackend returns a store-backed file store. store may be nil to use
// the store from the RunContext at call time.
func NewStoreBackend(namespace NamespaceFactory, store runtime.Store) *StoreBackend {
	return &StoreBackend{unimplemented: unimplemented{name: "StoreBackend"}, namespace: namespace, store: store}
}

// ValidateNamespace rejects empty components, wildcards and unsafe characters.
func ValidateNamespace(ns runtime.Namespace) error {
	return notimpl.Err("backend.ValidateNamespace")
}
