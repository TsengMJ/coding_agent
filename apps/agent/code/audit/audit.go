// Package audit records who did what on the remote service: logins, runs
// with their cost, approval decisions, workspace and pull request actions.
package audit

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Actions.
const (
	ActionLogin           = "login"
	ActionLogout          = "logout"
	ActionRun             = "run"
	ActionDecision        = "decision"
	ActionWorkspaceCreate = "workspace_create"
	ActionWorkspaceDelete = "workspace_delete"
	ActionPullRequest     = "pull_request"
)

// Event is one audit record.
type Event struct {
	Time        time.Time `json:"time"`
	User        string    `json:"user"`
	Action      string    `json:"action"`
	ThreadID    string    `json:"thread_id,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	// CostUSD is the model spend of a run.
	CostUSD float64        `json:"cost_usd,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Filter narrows List; empty fields match everything.
type Filter struct {
	User  string
	Since time.Time
	// Limit defaults to 100.
	Limit int
}

// Log stores audit events.
type Log interface {
	Record(ctx context.Context, ev Event) error
	// List returns matching events, newest first.
	List(ctx context.Context, f Filter) ([]Event, error)
	// SpentSince sums the run cost of user since t.
	SpentSince(ctx context.Context, user string, t time.Time) (float64, error)
}

// StartOfDay is the beginning of t's day in UTC, where daily limits reset.
func StartOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// MemoryLog keeps events in process memory, for tests.
type MemoryLog struct {
	mu     sync.Mutex
	events []Event
}

func NewMemoryLog() *MemoryLog { return &MemoryLog{} }

func (l *MemoryLog) Record(_ context.Context, ev Event) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return nil
}

func (l *MemoryLog) List(_ context.Context, f Filter) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var out []Event
	for _, ev := range slices.Backward(l.events) {
		if (f.User == "" || ev.User == f.User) && !ev.Time.Before(f.Since) {
			out = append(out, ev)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (l *MemoryLog) SpentSince(_ context.Context, user string, t time.Time) (float64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var sum float64
	for _, ev := range l.events {
		if ev.User == user && ev.Action == ActionRun && !ev.Time.Before(t) {
			sum += ev.CostUSD
		}
	}
	return sum, nil
}
