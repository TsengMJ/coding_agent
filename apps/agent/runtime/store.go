package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
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
	// Filter keeps items whose top-level value fields equal these values.
	Filter map[string]any
	// Query is semantic search, which no store here supports; set it and
	// Search fails.
	Query  string
	Limit  int
	Offset int
}

// Store is a cross-thread key-value store (LangGraph `BaseStore`). Get
// returns nil, nil for a missing key; Delete of a missing key succeeds.
type Store interface {
	Get(ctx context.Context, ns Namespace, key string) (*Item, error)
	Put(ctx context.Context, ns Namespace, key string, value map[string]any) error
	Delete(ctx context.Context, ns Namespace, key string) error
	// Search lists items whose namespace starts with prefix, ordered by
	// namespace then key.
	Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error)
}

// namespaceSep joins namespace components in stored keys, so components
// may not contain it (LangGraph forbids periods for the same reason).
const namespaceSep = "."

// ValidateNamespace rejects empty namespaces and components that are empty
// or contain ".", "|" or glob characters.
func ValidateNamespace(ns Namespace) error {
	if len(ns) == 0 {
		return errors.New("store: namespace must not be empty")
	}
	for _, c := range ns {
		if c == "" {
			return fmt.Errorf("store: namespace %q has an empty component", ns)
		}
		if strings.ContainsAny(c, ".|*?[]") {
			return fmt.Errorf("store: namespace component %q contains one of . | * ? [ ]", c)
		}
	}
	return nil
}

func joinNamespace(ns Namespace) string { return strings.Join(ns, namespaceSep) }

func splitNamespace(s string) Namespace { return Namespace(strings.Split(s, namespaceSep)) }

// hasNamespacePrefix reports whether ns starts with every component of prefix.
func hasNamespacePrefix(ns, prefix Namespace) bool {
	return len(ns) >= len(prefix) && slices.Equal(ns[:len(prefix)], prefix)
}

func checkSearch(prefix Namespace, opts SearchOptions) error {
	if opts.Query != "" {
		return errors.New("store: semantic search (SearchOptions.Query) is not supported")
	}
	if len(prefix) == 0 {
		return nil
	}
	return ValidateNamespace(prefix)
}

// finishSearch sorts, filters and pages items gathered by a store.
func finishSearch(items []Item, opts SearchOptions) []Item {
	items = slices.DeleteFunc(items, func(it Item) bool { return !matchesFilter(it.Value, opts.Filter) })
	slices.SortFunc(items, func(a, b Item) int {
		if c := strings.Compare(joinNamespace(a.Namespace), joinNamespace(b.Namespace)); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	if opts.Offset > 0 {
		items = items[min(opts.Offset, len(items)):]
	}
	if opts.Limit > 0 && len(items) > opts.Limit {
		items = items[:opts.Limit]
	}
	return items
}

// matchesFilter compares through JSON so values read back from a database
// (float64 numbers, []any) equal the Go values the caller passed.
func matchesFilter(v, filter map[string]any) bool {
	for k, want := range filter {
		got, ok := v[k]
		if !ok || !reflect.DeepEqual(normalizeJSON(got), normalizeJSON(want)) {
			return false
		}
	}
	return true
}

func normalizeJSON(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return v
	}
	return out
}

// cloneValue deep-copies a value through JSON, as a database round trip would.
func cloneValue(v map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("store: encode value: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("store: decode value: %w", err)
	}
	return out, nil
}

// InMemoryStore keeps items in process memory (LangGraph `InMemoryStore`).
type InMemoryStore struct {
	mu    sync.Mutex
	items map[string]Item
}

// NewInMemoryStore returns an empty in-memory store.
func NewInMemoryStore() *InMemoryStore { return &InMemoryStore{items: map[string]Item{}} }

func memoryKey(ns Namespace, key string) string { return joinNamespace(ns) + "|" + key }

func (s *InMemoryStore) Get(ctx context.Context, ns Namespace, key string) (*Item, error) {
	if err := ValidateNamespace(ns); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[memoryKey(ns, key)]
	if !ok {
		return nil, nil
	}
	v, err := cloneValue(it.Value)
	if err != nil {
		return nil, err
	}
	it.Value = v
	return &it, nil
}

func (s *InMemoryStore) Put(ctx context.Context, ns Namespace, key string, value map[string]any) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	v, err := cloneValue(value)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	it := Item{Namespace: slices.Clone(ns), Key: key, Value: v, CreatedAt: now, UpdatedAt: now}
	if prev, ok := s.items[memoryKey(ns, key)]; ok {
		it.CreatedAt = prev.CreatedAt
	}
	s.items[memoryKey(ns, key)] = it
	return nil
}

func (s *InMemoryStore) Delete(ctx context.Context, ns Namespace, key string) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, memoryKey(ns, key))
	return nil
}

func (s *InMemoryStore) Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error) {
	if err := checkSearch(prefix, opts); err != nil {
		return nil, err
	}
	s.mu.Lock()
	var out []Item
	for _, it := range s.items {
		if hasNamespacePrefix(it.Namespace, prefix) {
			out = append(out, it)
		}
	}
	s.mu.Unlock()
	for i := range out {
		v, err := cloneValue(out[i].Value)
		if err != nil {
			return nil, err
		}
		out[i].Value = v
	}
	return finishSearch(out, opts), nil
}
