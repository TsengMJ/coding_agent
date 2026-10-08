package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SolveResult is the agent phase outcome for one attempt.
type SolveResult struct {
	Patch string
	// Category is set when the agent stopped abnormally (agent_timeout,
	// agent_error, model_error, budget_exceeded, infra_error).
	Category  string
	Error     string
	Metrics   Metrics
	TracePath string
}

// SolveEnv is what a solver gets for one attempt.
type SolveEnv struct {
	RunID string
	Trial int
	// WorkDir is a fresh per-attempt directory the solver may use.
	WorkDir string
	// LogDir keeps attempt artifacts such as the agent trace.
	LogDir string
	Docker Docker
}

// Solver produces a patch for a task. A returned error aborts the attempt
// as an infrastructure failure.
type Solver interface {
	Name() string
	Solve(ctx context.Context, task Task, env SolveEnv) (SolveResult, error)
}

// OracleSolver returns the reference patch (release-gate sanity check).
type OracleSolver struct{}

func (OracleSolver) Name() string { return SolverOracle }

func (OracleSolver) Solve(_ context.Context, task Task, _ SolveEnv) (SolveResult, error) {
	p, err := task.GoldPatch()
	return SolveResult{Patch: p}, err
}

// NopSolver returns an empty patch; with the verifier run anyway, no task
// may pass.
type NopSolver struct{}

func (NopSolver) Name() string { return SolverNop }

func (NopSolver) Solve(context.Context, Task, SolveEnv) (SolveResult, error) {
	return SolveResult{}, nil
}

// Runner executes a run: every task in ids times Trials, resumable from the
// attempts already stored.
type Runner struct {
	Store   Store
	Docker  Docker
	Catalog *Catalog
	Solver  Solver
	// LogRoot holds per-attempt logs: <LogRoot>/<run>/<instance>/t<trial>.
	LogRoot     string
	Concurrency int
	// RetryInfra re-runs attempts that ended in infra_error.
	RetryInfra bool
	// PruneImages removes each task image after its last attempt.
	PruneImages bool
	Progress    io.Writer
	now         func() time.Time
}

// RunSummary counts attempt outcomes of one Run call.
type RunSummary struct {
	Total    int
	Skipped  int
	Resolved int
	Infra    int
}

// Run executes the run; it returns ctx.Err() when interrupted, leaving the
// run partial and resumable.
func (r *Runner) Run(ctx context.Context, run *Run, ids []string) (RunSummary, error) {
	if r.now == nil {
		r.now = time.Now
	}
	trials := max(run.Trials, 1)
	existing, err := r.Store.Attempts(ctx, run.ID)
	if err != nil {
		return RunSummary{}, err
	}
	prior := map[AttemptKey]Attempt{}
	for _, a := range existing {
		prior[AttemptKey{a.InstanceID, a.Trial}] = a
	}
	type job struct {
		task  Task
		trial int
		prior *Attempt
		last  bool
	}
	var jobs []job
	var sum RunSummary
	for _, id := range ids {
		task, ok := r.Catalog.Task(id)
		if !ok {
			return sum, fmt.Errorf("bench: unknown task %s", id)
		}
		for trial := range trials {
			sum.Total++
			j := job{task: task, trial: trial, last: trial == trials-1}
			if a, ok := prior[AttemptKey{id, trial}]; ok {
				if a.Phase == PhaseDone && (a.FailureCategory != CategoryInfraError || !r.RetryInfra) {
					sum.Skipped++
					if a.Resolved {
						sum.Resolved++
					}
					continue
				}
				if a.Phase == PhaseAgentDone {
					j.prior = &a
				}
			}
			jobs = append(jobs, j)
		}
	}
	r.logf("run %s: %d attempts, %d already done, %d to run (solver %s, concurrency %d)",
		run.ID, sum.Total, sum.Skipped, len(jobs), r.Solver.Name(), max(r.Concurrency, 1))

	sem := make(chan struct{}, max(r.Concurrency, 1))
	var mu sync.Mutex
	var wg sync.WaitGroup
	var fatal error
	done := sum.Skipped
	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		if ctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			a, err := r.attempt(ctx, run, j.task, j.trial, j.prior)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if fatal == nil && !errors.Is(err, context.Canceled) {
					fatal = err
				}
				return
			}
			done++
			if a.Resolved {
				sum.Resolved++
			}
			if a.FailureCategory == CategoryInfraError {
				sum.Infra++
			}
			r.logf("[%d/%d] %s t%d: %s%s", done, sum.Total, j.task.InstanceID, j.trial, describeAttempt(a), errSuffix(a.Error))
			if j.last && r.PruneImages {
				_ = r.Docker.RemoveImage(context.WithoutCancel(ctx), j.task.Image)
			}
		}()
	}
	wg.Wait()
	if fatal != nil {
		return sum, fatal
	}
	return sum, ctx.Err()
}

// attempt runs one trial and stores it after each phase. The returned error
// is a store failure or cancellation; task failures are recorded instead.
func (r *Runner) attempt(ctx context.Context, run *Run, task Task, trial int, prior *Attempt) (*Attempt, error) {
	logDir := filepath.Join(r.LogRoot, run.ID, task.InstanceID, fmt.Sprintf("t%d", trial))
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	a := &Attempt{RunID: run.ID, InstanceID: task.InstanceID, Trial: trial, Phase: PhasePending,
		StartedAt: r.now(), LogDir: logDir}
	agentCategory := ""
	if prior != nil {
		a = prior
		agentCategory = prior.FailureCategory
	} else {
		if err := r.Store.SaveAttempt(ctx, a); err != nil {
			return nil, err
		}
		digest, err := r.Docker.EnsureImage(ctx, task.Image)
		if err != nil {
			return r.infra(ctx, a, fmt.Errorf("image: %w", err))
		}
		a.ImageDigest = digest
		workDir := filepath.Join(logDir, "work")
		res, err := r.Solver.Solve(ctx, task, SolveEnv{RunID: run.ID, Trial: trial, WorkDir: workDir, LogDir: logDir, Docker: r.Docker})
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return r.infra(ctx, a, fmt.Errorf("agent phase: %w", err))
		}
		a.Patch, a.Metrics, a.TracePath, a.Error = res.Patch, res.Metrics, res.TracePath, res.Error
		agentCategory = res.Category
		if agentCategory == CategoryInfraError {
			return r.infra(ctx, a, errors.New(res.Error))
		}
		a.FailureCategory = agentCategory
		applyPatchStats(a, task)
		a.Phase = PhaseAgentDone
		if err := r.Store.SaveAttempt(ctx, a); err != nil {
			return nil, err
		}
	}
	g, err := Grader{Docker: r.Docker}.Grade(ctx, task, a.Patch, GradeOptions{LogDir: logDir, RunEmpty: r.Solver.Name() == SolverNop})
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return r.infra(ctx, a, fmt.Errorf("grading: %w", err))
	}
	a.GradeMS = g.Duration.Milliseconds()
	a.Resolved = g.Resolved
	a.F2PPassed, a.F2PTotal, a.P2PPassed, a.P2PTotal = g.F2PPassed, g.F2PTotal, g.P2PPassed, g.P2PTotal
	a.FailureCategory = Classify(agentCategory, g)
	if g.VerifierTimedOut && a.Error == "" {
		a.Error = "verifier timed out"
	}
	a.Phase = PhaseDone
	finished := r.now()
	a.FinishedAt = &finished
	if err := r.Store.SaveTestResults(ctx, run.ID, task.InstanceID, trial, g.Tests); err != nil {
		return nil, err
	}
	if err := r.Store.SaveAttempt(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

// ResetForRegrade moves the graded attempts of a run back to agent_done so
// the next Runner.Run grades their stored patches again without re-running
// the agent. Attempts that never produced a patch phase are left alone.
func ResetForRegrade(ctx context.Context, st Store, runID string) (int, error) {
	attempts, err := st.Attempts(ctx, runID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range attempts {
		a := &attempts[i]
		if a.Phase != PhaseDone || a.FailureCategory == CategoryInfraError {
			continue
		}
		if !IsAgentCategory(a.FailureCategory) {
			a.FailureCategory = ""
		}
		a.Phase, a.Resolved, a.FinishedAt = PhaseAgentDone, false, nil
		a.F2PPassed, a.F2PTotal, a.P2PPassed, a.P2PTotal = 0, 0, 0, 0
		if err := st.SaveAttempt(ctx, a); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (r *Runner) infra(ctx context.Context, a *Attempt, cause error) (*Attempt, error) {
	a.Phase = PhaseDone
	a.FailureCategory = CategoryInfraError
	a.Error = cause.Error()
	finished := r.now()
	a.FinishedAt = &finished
	if err := r.Store.SaveAttempt(context.WithoutCancel(ctx), a); err != nil {
		return nil, err
	}
	return a, nil
}

// applyPatchStats fills patch size and localization metrics.
func applyPatchStats(a *Attempt, task Task) {
	st := ParsePatch(a.Patch)
	a.PatchFiles, a.PatchAdded, a.PatchRemoved = len(st.Files), st.Added, st.Removed
	a.TouchesTests = len(st.TestFiles) > 0
	a.GoldPrecision, a.GoldRecall = nil, nil
	if p, rc, ok := FileOverlap(st.Files, task.GoldPaths); ok {
		a.GoldPrecision, a.GoldRecall = &p, &rc
	}
	if a.Process != nil {
		a.Process.GoldSrcRecall = nil
		if rc, ok := SourceRecall(st.Files, task.GoldPaths); ok {
			a.Process.GoldSrcRecall = &rc
		}
	}
}

func describeAttempt(a *Attempt) string {
	s := fmt.Sprintf("%s (f2p %d/%d, p2p %d/%d", a.FailureCategory, a.F2PPassed, a.F2PTotal, a.P2PPassed, a.P2PTotal)
	if a.AgentMS > 0 {
		s += ", agent " + (time.Duration(a.AgentMS) * time.Millisecond).Round(time.Second).String()
	}
	if a.GradeMS > 0 {
		s += ", grade " + (time.Duration(a.GradeMS) * time.Millisecond).Round(time.Second).String()
	}
	if a.CostUSD != nil {
		s += fmt.Sprintf(", $%.2f", *a.CostUSD)
	}
	return s + ")"
}

func errSuffix(e string) string {
	if e == "" {
		return ""
	}
	return ": " + clipTail(e, 200)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Progress != nil {
		fmt.Fprintf(r.Progress, format+"\n", args...)
	}
}
