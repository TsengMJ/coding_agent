// Package sessions catalogs conversation threads for listing and resume
// (Python `sessions.py`). Thread state itself lives in the checkpointer.
package sessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"time"
)

// titleMaxRunes caps the title derived from the first prompt.
const titleMaxRunes = 60

// Info summarizes one thread.
type Info struct {
	ThreadID     string    `json:"thread_id"`
	Cwd          string    `json:"cwd"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
	// Model is the "provider:model" spec the thread last ran with.
	Model string `json:"model,omitempty"`
	// Running is a process-local service status. It is intentionally not
	// persisted: a service restart cancels in-flight runs.
	Running bool `json:"running,omitempty"`
}

// Filter narrows List. An empty Cwd lists every thread; Limit <= 0 means no limit.
type Filter struct {
	Cwd   string
	Limit int
}

// Store is the thread catalog.
type Store interface {
	// Touch inserts or updates a thread. An existing thread keeps its Cwd,
	// CreatedAt, non-empty Title, and Model when info.Model is empty.
	Touch(ctx context.Context, info Info) error
	// Get returns nil when the thread is unknown.
	Get(ctx context.Context, threadID string) (*Info, error)
	// SetTitle changes an existing title and returns nil when the thread is unknown.
	SetTitle(ctx context.Context, threadID, title string) (*Info, error)
	// List returns threads, most recently updated first.
	List(ctx context.Context, f Filter) ([]Info, error)
	Delete(ctx context.Context, threadID string) error
}

// Latest returns the most recently updated thread for cwd, or nil.
func Latest(ctx context.Context, s Store, cwd string) (*Info, error) {
	list, err := s.List(ctx, Filter{Cwd: cwd, Limit: 1})
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// NewThreadID returns a fresh 12-hex-digit thread identifier.
func NewThreadID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sessions: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// Title derives a thread title from the first line of a prompt.
func Title(prompt string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	r := []rune(strings.TrimSpace(line))
	if len(r) > titleMaxRunes {
		return string(r[:titleMaxRunes]) + "..."
	}
	return string(r)
}

// MemoryStore keeps the catalog in process memory.
type MemoryStore struct {
	mu      sync.Mutex
	threads map[string]Info
}

// NewMemoryStore returns an empty catalog.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{threads: map[string]Info{}}
}

func (s *MemoryStore) Touch(_ context.Context, info Info) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if info.UpdatedAt.IsZero() {
		info.UpdatedAt = time.Now()
	}
	if prev, ok := s.threads[info.ThreadID]; ok {
		info.Cwd = prev.Cwd
		info.CreatedAt = prev.CreatedAt
		if prev.Title != "" {
			info.Title = prev.Title
		}
		if info.Model == "" {
			info.Model = prev.Model
		}
	} else if info.CreatedAt.IsZero() {
		info.CreatedAt = info.UpdatedAt
	}
	s.threads[info.ThreadID] = info
	return nil
}

func (s *MemoryStore) Get(_ context.Context, threadID string) (*Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.threads[threadID]
	if !ok {
		return nil, nil
	}
	return &info, nil
}

func (s *MemoryStore) SetTitle(_ context.Context, threadID, title string) (*Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, ok := s.threads[threadID]
	if !ok {
		return nil, nil
	}
	info.Title = title
	s.threads[threadID] = info
	return &info, nil
}

func (s *MemoryStore) List(_ context.Context, f Filter) ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Info
	for _, info := range s.threads {
		if f.Cwd == "" || info.Cwd == f.Cwd {
			out = append(out, info)
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *MemoryStore) Delete(_ context.Context, threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.threads, threadID)
	return nil
}
