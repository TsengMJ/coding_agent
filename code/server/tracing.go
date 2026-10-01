package server

import (
	"context"
	"log"
	"sync"

	"github.com/tsengmingchun/deepagents_golang/code/trace"
)

// runTrace connects one run to its thread's trace recorder. Records are
// written from the agent's goroutines, so they are queued and sent as
// EventTrace from the run's own goroutine. A nil *runTrace is a no-op.
type runTrace struct {
	rec     *trace.Recorder
	mu      sync.Mutex
	pending []trace.Update
}

// startTrace returns ctx carrying the thread's recorder, or ctx unchanged
// when the thread's trace cannot be read.
func (s *Service) startTrace(ctx context.Context, threadID string) (context.Context, *runTrace) {
	rec, err := s.traces.Recorder(threadID)
	if err != nil {
		log.Printf("dcode: tracing is off for thread %s: %v", threadID, err)
		return ctx, nil
	}
	t := &runTrace{rec: rec}
	rec.Listen(func(u trace.Update) {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.pending = append(t.pending, u)
	})
	return trace.WithRecorder(ctx, rec), t
}

func (t *runTrace) stop() {
	if t != nil {
		t.rec.Listen(nil)
	}
}

// flush sends queued records; false means the client stopped listening.
func (t *runTrace) flush(yield func(Event, error) bool) bool {
	if t == nil {
		return true
	}
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	for i := range pending {
		if !yield(Event{Kind: EventTrace, Trace: &pending[i]}, nil) {
			return false
		}
	}
	return true
}

// subagentMessage keeps messages of subagent runs, which are not part of
// thread state and would otherwise be gone once the run ends.
func (t *runTrace) subagentMessage(ev Event) {
	if t == nil || ev.Kind != EventMessage || len(ev.Namespace) == 0 || ev.Message == nil {
		return
	}
	if err := t.rec.RecordMessage(ev.Namespace, *ev.Message); err != nil {
		log.Printf("dcode: trace: %v", err)
	}
}
