package runtime

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/smallnest/langgraphgo/graph"
	"github.com/tsengmingchun/quill/llm"
)

// Checkpoint is a persisted snapshot of one thread's state.
//
// Seq, Steps, and ResumeFrom are typed so a JSON round trip (Postgres)
// does not turn them into float64 or []any.
type Checkpoint struct {
	ID        string         `json:"id,omitempty"`
	ThreadID  string         `json:"thread_id"`
	ParentID  string         `json:"parent_id,omitempty"`
	State     State          `json:"state"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	// PendingInterrupts are interrupts awaiting a resume value.
	PendingInterrupts []Interrupt `json:"pending_interrupts,omitempty"`
	// Seq orders checkpoints within a thread. Put assigns it.
	Seq int64 `json:"seq"`
	// Steps is the model-call count to restore on resume.
	Steps int `json:"steps,omitempty"`
	// ResumeFrom names the graph nodes to continue from after an interrupt.
	ResumeFrom []string `json:"resume_from,omitempty"`
}

// Checkpointer persists thread state between runs (LangGraph `Checkpointer`).
type Checkpointer interface {
	// Latest returns the newest checkpoint for a thread, or nil if none exists.
	Latest(ctx context.Context, threadID string) (*Checkpoint, error)
	Put(ctx context.Context, cp Checkpoint) error
	List(ctx context.Context, threadID string, limit int) ([]Checkpoint, error)
	DeleteThread(ctx context.Context, threadID string) error
}

// HistoryPage is the UI-safe subset of the latest checkpoint.  It keeps the
// agent checkpoint format intact while allowing callers to read a window of
// conversation messages instead of materialising the whole state.
type HistoryPage struct {
	ThreadID          string
	CheckpointSeq     int64
	Messages          []llm.Message
	PendingInterrupts []Interrupt
	// Values contains only values requested by the history reader (currently
	// the plan and todo state). It deliberately excludes large project state.
	Values     map[string]any
	HasMore    bool
	NextBefore int
}

// HistoryPager is implemented by checkpointers that can page messages at the
// storage layer. before is a one-based exclusive message position; zero asks
// for the newest page. limit is measured in user turns.
type HistoryPager interface {
	HistoryPage(ctx context.Context, threadID string, before, limit int) (*HistoryPage, error)
}

// LoadHistoryPage uses storage pagination where available. Memory and custom
// checkpointers retain the same API by slicing their latest checkpoint.
func LoadHistoryPage(ctx context.Context, c Checkpointer, threadID string, before, limit int) (*HistoryPage, error) {
	if p, ok := c.(HistoryPager); ok {
		return p.HistoryPage(ctx, threadID, before, limit)
	}
	cp, err := c.Latest(ctx, threadID)
	if err != nil || cp == nil {
		return nil, err
	}
	return pageCheckpoint(cp, before, limit), nil
}

// SyntheticSourceKey in a message's metadata names the middleware that
// added it (a summary, a gate's reminder); such a user message does not
// start a turn.
const SyntheticSourceKey = "lc_source"

// IsUserTurn reports whether m starts a user turn: a user message that a
// person, not middleware, sent.
func IsUserTurn(m llm.Message) bool {
	return m.Role == llm.RoleUser && m.Metadata[SyntheticSourceKey] == nil
}

func pageCheckpoint(cp *Checkpoint, before, limit int) *HistoryPage {
	msgs := cp.State.Messages
	upper := before
	if upper <= 0 || upper > len(msgs)+1 {
		upper = len(msgs) + 1
	}
	if limit <= 0 {
		limit = 20
	}
	start := 0
	seen := 0
	for i := upper - 2; i >= 0; i-- {
		if IsUserTurn(msgs[i]) {
			seen++
			if seen == limit {
				start = i
				break
			}
		}
	}
	page := slices.Clone(msgs[start : upper-1])
	values := map[string]any{}
	for _, key := range []string{"plan", "todos"} {
		if v, ok := cp.State.Values[key]; ok {
			values[key] = v
		}
	}
	return &HistoryPage{ThreadID: cp.ThreadID, CheckpointSeq: cp.Seq, Messages: page,
		PendingInterrupts: slices.Clone(cp.PendingInterrupts), Values: values,
		HasMore: start > 0, NextBefore: start + 1}
}

// DefaultCheckpointsPerThread is how many checkpoints Put keeps per thread.
// Only the newest one is read back (resume and pending interrupts); the rest
// are a short history for debugging.
const DefaultCheckpointsPerThread = 10

// checkpointIndex answers the per-thread queries Put and Latest need without
// loading every checkpoint. The Postgres store implements it in SQL; without
// one, storeCheckpointer falls back to scanning store.List.
type checkpointIndex interface {
	// latest returns the newest stored checkpoint, or nil.
	latest(ctx context.Context, threadID string) (*graph.Checkpoint, error)
	// maxSeq returns the highest seq stored for the thread, or 0.
	maxSeq(ctx context.Context, threadID string) (int64, error)
	// prune deletes all but the newest keep checkpoints.
	prune(ctx context.Context, threadID string, keep int) error
}

// storeCheckpointer implements Checkpointer on a langgraphgo CheckpointStore.
// Memory and Postgres share it. Put stores a runtime.Checkpoint as the
// graph checkpoint state, sets execution_id to the thread id, and stores
// Seq in the graph checkpoint Version so the index can order by it.
type storeCheckpointer struct {
	store graph.CheckpointStore
	index checkpointIndex
	keep  int

	mu   sync.Mutex
	seqs map[string]int64
}

func newStoreCheckpointer(store graph.CheckpointStore, index checkpointIndex) storeCheckpointer {
	return storeCheckpointer{store: store, index: index, keep: DefaultCheckpointsPerThread, seqs: map[string]int64{}}
}

// MemoryCheckpointer keeps checkpoints in langgraphgo's in-memory store
// (LangGraph `InMemorySaver`).
type MemoryCheckpointer struct {
	storeCheckpointer
}

// NewMemoryCheckpointer returns an empty in-memory checkpointer.
func NewMemoryCheckpointer() *MemoryCheckpointer {
	return &MemoryCheckpointer{storeCheckpointer: newStoreCheckpointer(graph.NewMemoryCheckpointStore(), nil)}
}

func (c *storeCheckpointer) Latest(ctx context.Context, threadID string) (*Checkpoint, error) {
	if c.index == nil {
		all, err := c.List(ctx, threadID, 1)
		if err != nil || len(all) == 0 {
			return nil, err
		}
		cp := all[0]
		return &cp, nil
	}
	raw, err := c.index.latest(ctx, threadID)
	if err != nil || raw == nil {
		return nil, err
	}
	cp, err := decodeCheckpoint(raw.State)
	if err != nil {
		return nil, fmt.Errorf("checkpoint %s: %w", raw.ID, err)
	}
	return &cp, nil
}

func (c *storeCheckpointer) Put(ctx context.Context, cp Checkpoint) error {
	cp.State = *cp.State.Clone()
	cp.PendingInterrupts = slices.Clone(cp.PendingInterrupts)
	cp.ResumeFrom = slices.Clone(cp.ResumeFrom)
	if cp.Metadata != nil {
		cp.Metadata = maps.Clone(cp.Metadata)
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	seq, err := c.nextSeq(ctx, cp.ThreadID)
	if err != nil {
		return err
	}
	cp.Seq = seq
	if cp.ID == "" {
		cp.ID = fmt.Sprintf("%s:%d:%d", cp.ThreadID, seq, cp.CreatedAt.UnixNano())
	}
	if err := c.store.Save(ctx, &graph.Checkpoint{
		ID:        cp.ID,
		State:     cp,
		Timestamp: cp.CreatedAt,
		Version:   int(seq),
		Metadata:  map[string]any{"execution_id": cp.ThreadID},
	}); err != nil {
		return err
	}
	if c.keep <= 0 {
		return nil
	}
	if err := c.prune(ctx, cp.ThreadID); err != nil {
		return fmt.Errorf("prune checkpoints for %s: %w", cp.ThreadID, err)
	}
	return nil
}

func (c *storeCheckpointer) prune(ctx context.Context, threadID string) error {
	if c.index != nil {
		return c.index.prune(ctx, threadID, c.keep)
	}
	raw, err := c.store.List(ctx, threadID)
	if err != nil || len(raw) <= c.keep {
		return err
	}
	slices.SortFunc(raw, func(a, b *graph.Checkpoint) int {
		if n := cmp.Compare(b.Version, a.Version); n != 0 {
			return n
		}
		return b.Timestamp.Compare(a.Timestamp)
	})
	for _, item := range raw[c.keep:] {
		if err := c.store.Delete(ctx, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *storeCheckpointer) List(ctx context.Context, threadID string, limit int) ([]Checkpoint, error) {
	raw, err := c.store.List(ctx, threadID)
	if err != nil {
		return nil, err
	}
	out := make([]Checkpoint, 0, len(raw))
	for _, item := range raw {
		cp, err := decodeCheckpoint(item.State)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s: %w", item.ID, err)
		}
		out = append(out, cp)
	}
	slices.SortFunc(out, func(a, b Checkpoint) int {
		if n := cmp.Compare(b.Seq, a.Seq); n != 0 {
			return n
		}
		return b.CreatedAt.Compare(a.CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (c *storeCheckpointer) DeleteThread(ctx context.Context, threadID string) error {
	c.mu.Lock()
	delete(c.seqs, threadID)
	c.mu.Unlock()
	return c.store.Clear(ctx, threadID)
}

// nextSeq is one past the newest seq for the thread. The store is asked once
// per thread per process; later calls use the cached value, so a thread must
// have a single writer (the server's per-thread lock guarantees that).
func (c *storeCheckpointer) nextSeq(ctx context.Context, threadID string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cur, ok := c.seqs[threadID]
	if !ok {
		stored, err := c.storedMaxSeq(ctx, threadID)
		if err != nil {
			return 0, err
		}
		cur = stored
	}
	cur++
	c.seqs[threadID] = cur
	return cur, nil
}

func (c *storeCheckpointer) storedMaxSeq(ctx context.Context, threadID string) (int64, error) {
	if c.index != nil {
		return c.index.maxSeq(ctx, threadID)
	}
	raw, err := c.store.List(ctx, threadID)
	if err != nil {
		return 0, err
	}
	var top int64
	for _, item := range raw {
		cp, err := decodeCheckpoint(item.State)
		if err != nil {
			return 0, fmt.Errorf("checkpoint %s: %w", item.ID, err)
		}
		top = max(top, cp.Seq)
	}
	return top, nil
}

// decodeCheckpoint accepts a Checkpoint stored in memory, or the
// map[string]any langgraphgo returns after a JSON round trip.
func decodeCheckpoint(state any) (Checkpoint, error) {
	switch s := state.(type) {
	case Checkpoint:
		return s, nil
	case *Checkpoint:
		if s == nil {
			return Checkpoint{}, fmt.Errorf("nil checkpoint state")
		}
		return *s, nil
	case nil:
		return Checkpoint{}, fmt.Errorf("nil checkpoint state")
	default:
		raw, err := json.Marshal(s)
		if err != nil {
			return Checkpoint{}, err
		}
		var cp Checkpoint
		if err := json.Unmarshal(raw, &cp); err != nil {
			return Checkpoint{}, err
		}
		return cp, nil
	}
}
