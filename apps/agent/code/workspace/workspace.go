// Package workspace gives each thread of the remote service its own clone of
// a repository and a container to run commands in. Files live on the host
// under Config.Root/<id> and are bind-mounted into the container at
// ContainerDir, so file tools read the host copy while shell commands run in
// the container with only the image's environment.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
)

// ContainerDir is where the repository is mounted inside the container.
const ContainerDir = "/workspace"

// State is a workspace container's last known state.
type State string

const (
	StateStopped State = "stopped"
	StateRunning State = "running"
)

var (
	// ErrNotFound is returned for an unknown workspace.
	ErrNotFound = errors.New("workspace not found")
	// ErrInvalid wraps a malformed workspace spec.
	ErrInvalid = errors.New("invalid workspace spec")
	// ErrBusy is returned when a workspace in use is stopped or deleted.
	ErrBusy = errors.New("workspace is in use by a run")
	// ErrQuota is returned when an owner already has the maximum number of
	// running workspaces.
	ErrQuota = errors.New("too many running workspaces")
	// ErrForbidden is returned when a user touches another owner's workspace.
	ErrForbidden = errors.New("workspace belongs to another user")
)

// Spec describes the repository to clone.
type Spec struct {
	RepoURL    string `json:"repo_url"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// Workspace is one cloned repository and its container.
type Workspace struct {
	ID    string `json:"id"`
	Owner string `json:"owner,omitempty"`
	Spec
	// Image is the container image the workspace runs.
	Image string `json:"image"`
	// Dir is the clone on the server host.
	Dir        string    `json:"-"`
	State      State     `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	// InUse is a process-local status: a run currently holds the workspace.
	InUse bool `json:"in_use,omitempty"`
}

// ContainerName is the Docker name of a workspace's container.
func ContainerName(id string) string { return "quill-ws-" + id }

// Store persists workspaces.
type Store interface {
	Put(ctx context.Context, ws Workspace) error
	// Get returns ErrNotFound for an unknown id.
	Get(ctx context.Context, id string) (*Workspace, error)
	// List returns the owner's workspaces, newest first; an empty owner
	// lists every workspace.
	List(ctx context.Context, owner string) ([]Workspace, error)
	Delete(ctx context.Context, id string) error
}

// NewID returns a fresh 12-hex-digit workspace identifier.
func NewID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("workspace: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// MemoryStore keeps workspaces in process memory, for tests.
type MemoryStore struct {
	mu  sync.Mutex
	all map[string]Workspace
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{all: map[string]Workspace{}} }

func (s *MemoryStore) Put(_ context.Context, ws Workspace) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws.InUse = false
	s.all[ws.ID] = ws
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (*Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok := s.all[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &ws, nil
}

func (s *MemoryStore) List(_ context.Context, owner string) ([]Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Workspace
	for _, ws := range s.all {
		if owner == "" || ws.Owner == owner {
			out = append(out, ws)
		}
	}
	slices.SortFunc(out, func(a, b Workspace) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, id)
	return nil
}
