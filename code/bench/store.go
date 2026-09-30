package bench

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
)

// Solvers.
const (
	SolverDcode  = "dcode"
	SolverOracle = "oracle"
	SolverNop    = "nop"
)

// Run statuses.
const (
	RunRunning   = "running"
	RunCompleted = "completed"
	RunPartial   = "partial"
	RunFailed    = "failed"
)

// Attempt phases; an attempt resumes from its phase.
const (
	PhasePending   = "pending"
	PhaseAgentDone = "agent_done"
	PhaseDone      = "done"
)

// Run is one dbench invocation over a subset.
type Run struct {
	ID              string
	CreatedAt       time.Time
	Benchmark       string
	Subset          string
	Solver          string
	Model           string
	Trials          int
	Concurrency     int
	GitCommit       string
	GitDirty        bool
	Config          json.RawMessage
	TasksRepoCommit string
	DatasetRevision string
	Host            json.RawMessage
	Status          string
	StartedAt       time.Time
	FinishedAt      *time.Time
	Notes           string
}

// Attempt is one trial of one task within a run.
type Attempt struct {
	RunID           string
	InstanceID      string
	Trial           int
	Phase           string
	Resolved        bool
	F2PPassed       int
	F2PTotal        int
	P2PPassed       int
	P2PTotal        int
	FailureCategory string
	Error           string
	Patch           string
	PatchFiles      int
	PatchAdded      int
	PatchRemoved    int
	TouchesTests    bool
	// GoldPrecision and GoldRecall are nil when the overlap is undefined
	// (empty patch or test-only changes).
	GoldPrecision *float64
	GoldRecall    *float64
	Metrics
	ImageDigest string
	TracePath   string
	LogDir      string
	StartedAt   time.Time
	FinishedAt  *time.Time
}

// Metrics describe how the agent worked on a task.
type Metrics struct {
	Steps             int
	LLMCalls          int
	ToolCalls         map[string]int
	ExecuteCalls      int
	TestRuns          int
	FilesRead         int
	FirstEditStep     *int
	Summarizations    int
	PeakContextTokens int
	TokensIn          int
	TokensOut         int
	CacheRead         int
	CacheWrite        int
	// CostUSD is nil when the model has no price.
	CostUSD   *float64
	AgentMS   int64
	ModelMS   int64
	ToolMS    int64
	ExecuteMS int64
	GradeMS   int64
}

// TestResult is one required test's status after grading.
type TestResult struct {
	Name string
	// Kind is "f2p" or "p2p".
	Kind string
	// Status is the parser status (PASSED, FAILED, SKIPPED, ERROR) or
	// MISSING when output.json has no entry.
	Status string
}

// Reference is a published per-task grade from another harness and model.
type Reference struct {
	Source     string
	Harness    string
	Model      string
	InstanceID string
	Resolved   bool
	Steps      *int
	CostUSD    *float64
	URL        string
	ImportedAt time.Time
}

// RunFilter selects runs for history.
type RunFilter struct {
	Subset string
	Model  string
	Solver string
	Limit  int
}

// Store persists the catalog, runs, attempts and references.
type Store interface {
	UpsertTasks(ctx context.Context, tasks []Task) error
	CreateRun(ctx context.Context, r *Run) error
	FinishRun(ctx context.Context, id, status string, at time.Time) error
	Run(ctx context.Context, id string) (*Run, error)
	Runs(ctx context.Context, f RunFilter) ([]Run, error)
	SaveAttempt(ctx context.Context, a *Attempt) error
	Attempts(ctx context.Context, runID string) ([]Attempt, error)
	SaveTestResults(ctx context.Context, runID, instanceID string, trial int, results []TestResult) error
	TestResults(ctx context.Context, runID string) (map[AttemptKey][]TestResult, error)
	ImportReferences(ctx context.Context, refs []Reference) (int, error)
	References(ctx context.Context, harness, model string) ([]Reference, error)
	ReferenceSets(ctx context.Context) ([][2]string, error)
}

// AttemptKey identifies an attempt within a run.
type AttemptKey struct {
	InstanceID string
	Trial      int
}

// ErrRunNotFound is returned by Store.Run for an unknown run ID.
var ErrRunNotFound = errors.New("bench: run not found")

// MemoryStore is an in-process Store for tests and dry runs.
type MemoryStore struct {
	mu       sync.Mutex
	tasks    map[string]Task
	runs     map[string]Run
	attempts map[string]map[AttemptKey]Attempt
	tests    map[string]map[AttemptKey][]TestResult
	refs     map[[3]string]Reference
}

// NewMemoryStore returns an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tasks:    map[string]Task{},
		runs:     map[string]Run{},
		attempts: map[string]map[AttemptKey]Attempt{},
		tests:    map[string]map[AttemptKey][]TestResult{},
		refs:     map[[3]string]Reference{},
	}
}

func (m *MemoryStore) UpsertTasks(_ context.Context, tasks []Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range tasks {
		m.tasks[t.InstanceID] = t
	}
	return nil
}

func (m *MemoryStore) CreateRun(_ context.Context, r *Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	m.runs[r.ID] = *r
	return nil
}

func (m *MemoryStore) FinishRun(_ context.Context, id, status string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return ErrRunNotFound
	}
	r.Status = status
	r.FinishedAt = &at
	m.runs[id] = r
	return nil
}

func (m *MemoryStore) Run(_ context.Context, id string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return nil, ErrRunNotFound
	}
	return &r, nil
}

func (m *MemoryStore) Runs(_ context.Context, f RunFilter) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Run
	for _, r := range m.runs {
		if (f.Subset == "" || r.Subset == f.Subset) && (f.Model == "" || r.Model == f.Model) &&
			(f.Solver == "" || r.Solver == f.Solver) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, nil
}

func (m *MemoryStore) SaveAttempt(_ context.Context, a *Attempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts[a.RunID] == nil {
		m.attempts[a.RunID] = map[AttemptKey]Attempt{}
	}
	m.attempts[a.RunID][AttemptKey{a.InstanceID, a.Trial}] = *a
	return nil
}

func (m *MemoryStore) Attempts(_ context.Context, runID string) ([]Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Attempt
	for _, a := range m.attempts[runID] {
		out = append(out, a)
	}
	sortAttempts(out)
	return out, nil
}

func (m *MemoryStore) SaveTestResults(_ context.Context, runID, instanceID string, trial int, results []TestResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tests[runID] == nil {
		m.tests[runID] = map[AttemptKey][]TestResult{}
	}
	m.tests[runID][AttemptKey{instanceID, trial}] = slices.Clone(results)
	return nil
}

func (m *MemoryStore) TestResults(_ context.Context, runID string) (map[AttemptKey][]TestResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[AttemptKey][]TestResult{}
	for k, v := range m.tests[runID] {
		out[k] = slices.Clone(v)
	}
	return out, nil
}

func (m *MemoryStore) ImportReferences(_ context.Context, refs []Reference) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range refs {
		m.refs[[3]string{r.Harness, r.Model, r.InstanceID}] = r
	}
	return len(refs), nil
}

func (m *MemoryStore) References(_ context.Context, harness, model string) ([]Reference, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Reference
	for k, r := range m.refs {
		if k[0] == harness && k[1] == model {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstanceID < out[j].InstanceID })
	return out, nil
}

func (m *MemoryStore) ReferenceSets(_ context.Context) ([][2]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[[2]string]bool{}
	var out [][2]string
	for k := range m.refs {
		key := [2]string{k[0], k[1]}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i][0]+"\x00"+out[i][1] < out[j][0]+"\x00"+out[j][1]
	})
	return out, nil
}

func sortAttempts(as []Attempt) {
	sort.Slice(as, func(i, j int) bool {
		if as[i].InstanceID != as[j].InstanceID {
			return as[i].InstanceID < as[j].InstanceID
		}
		return as[i].Trial < as[j].Trial
	})
}
