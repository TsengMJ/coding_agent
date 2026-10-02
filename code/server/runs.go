package server

import (
	"context"
	"sync"
)

// liveRuns keeps the runs the HTTP handler started, so a detached run
// outlives the request that started it: a client that disconnects (a
// reloaded GUI tab) can attach again and replay what it missed, and only an
// explicit cancel or server shutdown stops the run.
type liveRuns struct {
	mu   sync.Mutex
	runs map[string]*liveRun
}

// liveRun buffers one run's events for every attached client.
type liveRun struct {
	cancel context.CancelFunc

	mu     sync.Mutex
	events []Event
	err    error
	done   bool
	// changed is closed and replaced whenever events or done change.
	changed chan struct{}
}

func newLiveRuns() *liveRuns { return &liveRuns{runs: map[string]*liveRun{}} }

// start runs req on srv under ctx, detached from the caller. It fails with
// ErrThreadBusy while the thread already has a run here.
func (l *liveRuns) start(ctx context.Context, srv Server, req RunRequest, wg *sync.WaitGroup) (*liveRun, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.runs[req.ThreadID] != nil {
		return nil, ErrThreadBusy
	}
	ctx, cancel := context.WithCancel(ctx)
	run := &liveRun{cancel: cancel, changed: make(chan struct{})}
	if req.ThreadID != "" {
		l.runs[req.ThreadID] = run
	}
	if wg != nil {
		wg.Add(1)
	}
	go func() {
		defer func() {
			cancel()
			l.mu.Lock()
			if l.runs[req.ThreadID] == run {
				delete(l.runs, req.ThreadID)
			}
			l.mu.Unlock()
			if wg != nil {
				wg.Done()
			}
		}()
		for ev, err := range srv.Run(ctx, req) {
			if err != nil {
				run.finish(err)
				return
			}
			run.add(ev)
			if ctx.Err() != nil {
				run.finish(ctx.Err())
				return
			}
		}
		run.finish(nil)
	}()
	return run, nil
}

// get returns the thread's unfinished run, or nil.
func (l *liveRuns) get(threadID string) *liveRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.runs[threadID]
}

func (r *liveRun) add(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *liveRun) finish(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.err, r.done = err, true
	close(r.changed)
}

// next returns the events from index from on, whether the run is done (and
// its error), and a channel closed on the next change.
func (r *liveRun) next(from int) ([]Event, bool, error, <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[min(from, len(r.events)):], r.done, r.err, r.changed
}

// follow sends every event of the run from the first, then waits for more,
// until the run ends, send fails or ctx is done. It reports the run's error
// and whether the run finished while following.
func (r *liveRun) follow(ctx context.Context, send func(Event) bool) (finished bool, err error) {
	pos := 0
	for {
		evs, done, runErr, changed := r.next(pos)
		for _, ev := range evs {
			if !send(ev) {
				return false, nil
			}
		}
		pos += len(evs)
		if done {
			return true, runErr
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return false, nil
		}
	}
}
