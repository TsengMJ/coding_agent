package bench

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	st, err := NewPostgresStore(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStoresRoundTrip(t *testing.T) {
	stores := map[string]func(t *testing.T) Store{
		"memory":   func(*testing.T) Store { return NewMemoryStore() },
		"postgres": func(t *testing.T) Store { return testPostgresStore(t) },
	}
	for name, mk := range stores {
		t.Run(name, func(t *testing.T) {
			st := mk(t)
			ctx := context.Background()
			runID := fmt.Sprintf("test-%d", time.Now().UnixNano())
			if pg, ok := st.(*PostgresStore); ok {
				t.Cleanup(func() {
					_, _ = pg.pool.Exec(ctx, `DELETE FROM bench_runs WHERE id=$1`, runID)
					_, _ = pg.pool.Exec(ctx, `DELETE FROM bench_tasks WHERE instance_id='instance_test_roundtrip'`)
				})
			}
			task := Task{InstanceID: "instance_test_roundtrip", Repo: "o/r", Language: "go", GoldPaths: []string{"a.go"},
				FailToPass: []string{"t1"}, Image: "img"}
			if err := st.UpsertTasks(ctx, []Task{task}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			if err := st.CreateRun(ctx, &Run{ID: runID, Benchmark: Benchmark, Subset: SubsetSmoke, Solver: SolverQuill,
				Model: "m", Trials: 1, Concurrency: 1, TasksRepoCommit: TasksRepoCommit, Status: RunRunning,
				StartedAt: now, Meta: []byte(`{"prompt_template_sha256": "abc"}`)}); err != nil {
				t.Fatal(err)
			}
			prec, cost, first := 0.5, 1.25, 3
			a := &Attempt{RunID: runID, InstanceID: task.InstanceID, Trial: 0, Phase: PhaseDone, Resolved: true,
				F2PPassed: 1, F2PTotal: 1, FailureCategory: CategoryResolved, Patch: "diff", GoldPrecision: &prec,
				Metrics: Metrics{Steps: 7, ToolCalls: map[string]int{"execute": 2}, CostUSD: &cost, FirstEditStep: &first,
					Process: &Process{DuplicateToolCalls: 4, ToolOutputChars: map[string]int{"grep": 10}, FirstGoldSrcTouchStep: &first}},
				StartedAt: now}
			if err := st.SaveAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
			a.Steps = 8
			if err := st.SaveAttempt(ctx, a); err != nil {
				t.Fatal(err)
			}
			got, err := st.Attempts(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Steps != 8 || got[0].ToolCalls["execute"] != 2 || *got[0].CostUSD != cost ||
				*got[0].GoldPrecision != prec || got[0].GoldRecall != nil || *got[0].FirstEditStep != 3 {
				t.Fatalf("attempts %+v", got)
			}
			if p := got[0].Process; p == nil || p.DuplicateToolCalls != 4 || p.ToolOutputChars["grep"] != 10 ||
				*p.FirstGoldSrcTouchStep != 3 || p.GoldSrcRecall != nil {
				t.Fatalf("process %+v", got[0].Process)
			}
			results := []TestResult{{Name: "t1", Kind: "f2p", Status: "PASSED"}, {Name: "t2", Kind: "p2p", Status: "MISSING"}}
			if err := st.SaveTestResults(ctx, runID, task.InstanceID, 0, results); err != nil {
				t.Fatal(err)
			}
			tr, err := st.TestResults(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if len(tr[AttemptKey{task.InstanceID, 0}]) != 2 {
				t.Fatalf("test results %+v", tr)
			}
			if err := st.FinishRun(ctx, runID, RunCompleted, now); err != nil {
				t.Fatal(err)
			}
			r, err := st.Run(ctx, runID)
			if err != nil || r.Status != RunCompleted || r.FinishedAt == nil || !strings.Contains(string(r.Meta), `"abc"`) {
				t.Fatalf("run %+v %v", r, err)
			}
			if _, err := st.Run(ctx, runID+"-missing"); err != ErrRunNotFound {
				t.Fatalf("missing run err %v", err)
			}
		})
	}
}
