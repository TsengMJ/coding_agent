package bench

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testCatalog(t *testing.T, ids ...string) *Catalog {
	t.Helper()
	root := t.TempDir()
	c := &Catalog{byID: map[string]Task{}}
	for _, id := range ids {
		task, err := LoadTask(writeTask(t, root, id))
		if err != nil {
			t.Fatal(err)
		}
		task.FailToPass, task.PassToPass = []string{"f"}, []string{"p"}
		c.Tasks = append(c.Tasks, task)
		c.byID[id] = task
	}
	return c
}

// countingSolver returns a fixed patch and counts calls.
type countingSolver struct {
	calls atomic.Int32
	res   SolveResult
	err   error
}

func (s *countingSolver) Name() string { return SolverQuill }

func (s *countingSolver) Solve(context.Context, Task, SolveEnv) (SolveResult, error) {
	s.calls.Add(1)
	return s.res, s.err
}

func newRun(id string, solver string) *Run {
	return &Run{ID: id, Benchmark: Benchmark, Subset: "test", Solver: solver, Trials: 1, Status: RunRunning, StartedAt: time.Now()}
}

func passWhenPatched(patch string) (string, map[string]string) {
	if patch == "" {
		return "0", map[string]string{"f": "FAILED", "p": "PASSED"}
	}
	return "1", map[string]string{"f": "PASSED", "p": "PASSED"}
}

func TestRunnerOracleAndNopGate(t *testing.T) {
	cat := testCatalog(t, "instance_a", "instance_b")
	d := newFakeDocker()
	d.verify = passWhenPatched
	st := NewMemoryStore()
	ctx := context.Background()
	for _, tc := range []struct {
		solver   Solver
		resolved int
	}{{OracleSolver{}, 2}, {NopSolver{}, 0}} {
		run := newRun("gate-"+tc.solver.Name(), tc.solver.Name())
		_ = st.CreateRun(ctx, run)
		r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: tc.solver, LogRoot: t.TempDir(), Concurrency: 2}
		sum, err := r.Run(ctx, run, []string{"instance_a", "instance_b"})
		if err != nil {
			t.Fatal(err)
		}
		if sum.Resolved != tc.resolved {
			t.Fatalf("%s resolved %d, want %d", tc.solver.Name(), sum.Resolved, tc.resolved)
		}
		as, _ := st.Attempts(ctx, run.ID)
		if len(as) != 2 || as[0].Phase != PhaseDone || as[0].ImageDigest == "" {
			t.Fatalf("attempts %+v", as)
		}
		tests, _ := st.TestResults(ctx, run.ID)
		if len(tests[AttemptKey{"instance_a", 0}]) != 2 {
			t.Fatalf("test results %+v", tests)
		}
	}
	oracle, _ := st.Attempts(ctx, "gate-oracle")
	if oracle[0].GoldRecall == nil || *oracle[0].GoldRecall != 1 || oracle[0].PatchFiles != 2 {
		t.Fatalf("oracle patch stats %+v", oracle[0])
	}
}

func TestRunnerResumesAndSkipsDone(t *testing.T) {
	cat := testCatalog(t, "instance_a", "instance_b")
	d := newFakeDocker()
	d.verify = passWhenPatched
	st := NewMemoryStore()
	ctx := context.Background()
	run := newRun("resume", SolverQuill)
	_ = st.CreateRun(ctx, run)
	// instance_a finished; instance_b has a patch awaiting grading.
	_ = st.SaveAttempt(ctx, &Attempt{RunID: run.ID, InstanceID: "instance_a", Phase: PhaseDone, Resolved: true,
		FailureCategory: CategoryResolved})
	_ = st.SaveAttempt(ctx, &Attempt{RunID: run.ID, InstanceID: "instance_b", Phase: PhaseAgentDone, Patch: "diff --git a/x b/x"})
	solver := &countingSolver{}
	r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: solver, LogRoot: t.TempDir()}
	sum, err := r.Run(ctx, run, []string{"instance_a", "instance_b"})
	if err != nil {
		t.Fatal(err)
	}
	if solver.calls.Load() != 0 {
		t.Fatalf("solver called %d times", solver.calls.Load())
	}
	if sum.Skipped != 1 || sum.Resolved != 2 {
		t.Fatalf("summary %+v", sum)
	}
}

func TestRunnerInfraErrorsAreRetryable(t *testing.T) {
	cat := testCatalog(t, "instance_a")
	d := newFakeDocker()
	d.verify = passWhenPatched
	d.imageErr = errors.New("pull failed")
	st := NewMemoryStore()
	ctx := context.Background()
	run := newRun("infra", SolverQuill)
	_ = st.CreateRun(ctx, run)
	solver := &countingSolver{res: SolveResult{Patch: "diff --git a/x b/x"}}
	r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: solver, LogRoot: t.TempDir()}
	sum, err := r.Run(ctx, run, []string{"instance_a"})
	if err != nil {
		t.Fatal(err)
	}
	as, _ := st.Attempts(ctx, run.ID)
	if sum.Infra != 1 || as[0].FailureCategory != CategoryInfraError || as[0].Phase != PhaseDone {
		t.Fatalf("summary %+v attempt %+v", sum, as[0])
	}
	d.imageErr = nil
	if sum, _ := r.Run(ctx, run, []string{"instance_a"}); sum.Skipped != 1 {
		t.Fatalf("without RetryInfra the attempt should be skipped: %+v", sum)
	}
	r.RetryInfra = true
	if _, err := r.Run(ctx, run, []string{"instance_a"}); err != nil {
		t.Fatal(err)
	}
	as, _ = st.Attempts(ctx, run.ID)
	if !as[0].Resolved || as[0].FailureCategory != CategoryResolved {
		t.Fatalf("retried attempt %+v", as[0])
	}
}

func TestRunnerAgentCategoryAndPrune(t *testing.T) {
	cat := testCatalog(t, "instance_a")
	d := newFakeDocker()
	st := NewMemoryStore()
	ctx := context.Background()
	run := newRun("timeout", SolverQuill)
	run.Trials = 2
	_ = st.CreateRun(ctx, run)
	solver := &countingSolver{res: SolveResult{Patch: "diff --git a/src/a.js b/src/a.js\n", Category: CategoryAgentTimeout}}
	r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: solver, LogRoot: t.TempDir(), PruneImages: true}
	if _, err := r.Run(ctx, run, []string{"instance_a"}); err != nil {
		t.Fatal(err)
	}
	as, _ := st.Attempts(ctx, run.ID)
	if len(as) != 2 || as[1].Trial != 1 || as[0].FailureCategory != CategoryAgentTimeout {
		t.Fatalf("attempts %+v", as)
	}
	if as[0].GoldPrecision == nil || *as[0].GoldPrecision != 1 || *as[0].GoldRecall != 1 {
		t.Fatalf("localization %+v %+v", as[0].GoldPrecision, as[0].GoldRecall)
	}
	if d.called("rmi") != 1 {
		t.Fatalf("image should be pruned once after the last trial: %v", d.calls)
	}
	if filepath.Base(as[1].LogDir) != "t1" {
		t.Fatalf("log dir %s", as[1].LogDir)
	}
}

func TestResetForRegradeGradesStoredPatchesAgain(t *testing.T) {
	cat := testCatalog(t, "instance_a", "instance_b")
	d := newFakeDocker()
	st := NewMemoryStore()
	ctx := context.Background()
	run := newRun("regrade", SolverQuill)
	_ = st.CreateRun(ctx, run)
	_ = st.SaveAttempt(ctx, &Attempt{RunID: run.ID, InstanceID: "instance_a", Phase: PhaseDone, Patch: "diff --git a/x b/x",
		FailureCategory: CategoryIncompleteFix, F2PTotal: 1})
	_ = st.SaveAttempt(ctx, &Attempt{RunID: run.ID, InstanceID: "instance_b", Phase: PhaseDone, Patch: "diff --git a/y b/y",
		FailureCategory: CategoryAgentTimeout})
	n, err := ResetForRegrade(ctx, st, run.ID)
	if err != nil || n != 2 {
		t.Fatalf("reset %d %v", n, err)
	}
	d.verify = passWhenPatched
	solver := &countingSolver{}
	r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: solver, LogRoot: t.TempDir()}
	sum, err := r.Run(ctx, run, []string{"instance_a", "instance_b"})
	if err != nil {
		t.Fatal(err)
	}
	if solver.calls.Load() != 0 || sum.Resolved != 2 {
		t.Fatalf("summary %+v solver calls %d", sum, solver.calls.Load())
	}
	d.verify = func(string) (string, map[string]string) { return "0", map[string]string{"f": "FAILED", "p": "PASSED"} }
	_, _ = ResetForRegrade(ctx, st, run.ID)
	_, _ = r.Run(ctx, run, []string{"instance_a", "instance_b"})
	as, _ := st.Attempts(ctx, run.ID)
	if as[0].FailureCategory != CategoryIncompleteFix || as[1].FailureCategory != CategoryIncompleteFix {
		t.Fatalf("categories %s %s", as[0].FailureCategory, as[1].FailureCategory)
	}
}

func TestRunnerStopsOnCancel(t *testing.T) {
	cat := testCatalog(t, "instance_a", "instance_b")
	d := newFakeDocker()
	st := NewMemoryStore()
	ctx, cancel := context.WithCancel(context.Background())
	run := newRun("cancel", SolverQuill)
	_ = st.CreateRun(ctx, run)
	solver := &cancelSolver{cancel: cancel}
	r := &Runner{Store: st, Docker: d, Catalog: cat, Solver: solver, LogRoot: t.TempDir()}
	if _, err := r.Run(ctx, run, []string{"instance_a", "instance_b"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if solver.calls != 1 {
		t.Fatalf("solver ran %d times after cancel", solver.calls)
	}
}

type cancelSolver struct {
	cancel func()
	calls  int
}

func (s *cancelSolver) Name() string { return SolverQuill }

func (s *cancelSolver) Solve(context.Context, Task, SolveEnv) (SolveResult, error) {
	s.calls++
	s.cancel()
	return SolveResult{}, nil
}
