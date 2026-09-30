package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/code/bench"
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// subsetIDs resolves -subset and applies -limit (0 = all).
func subsetIDs(c *bench.Catalog, subset string, limit int) ([]string, error) {
	ids, err := bench.ResolveSubset(subset, c.Tasks, c.V2Dir)
	if err != nil {
		return nil, err
	}
	if limit > 0 && limit < len(ids) {
		ids = ids[:limit]
	}
	return ids, nil
}

// newRun builds a run record carrying everything needed to reproduce it.
func newRun(ctx context.Context, e env, c *bench.Catalog, d bench.CLIDocker, solver, subset, model string, trials, concurrency int, config any) (*bench.Run, error) {
	now := time.Now().UTC()
	cfg, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	commit, dirty := gitState(ctx, e.cwd)
	host := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "go": runtime.Version()}
	if info, err := d.Version(ctx); err == nil {
		for k, v := range info {
			host["docker_"+k] = v
		}
	}
	hostJSON, _ := json.Marshal(host)
	return &bench.Run{
		ID:              fmt.Sprintf("%s-%s-%s", now.Format("20060102-150405"), solver, subsetLabel(subset)),
		Benchmark:       bench.Benchmark,
		Subset:          subset,
		Solver:          solver,
		Model:           model,
		Trials:          trials,
		Concurrency:     concurrency,
		GitCommit:       commit,
		GitDirty:        dirty,
		Config:          cfg,
		TasksRepoCommit: c.TasksRepoCommit,
		DatasetRevision: c.DatasetRevision,
		Host:            hostJSON,
		Status:          bench.RunRunning,
		StartedAt:       now,
	}, nil
}

// subsetLabel shortens file-path subsets for run IDs.
func subsetLabel(subset string) string {
	return strings.TrimSuffix(filepath.Base(subset), ".txt")
}

func gitState(ctx context.Context, dir string) (commit string, dirty bool) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", false
	}
	status, _ := exec.CommandContext(ctx, "git", "-C", dir, "status", "--porcelain", "--untracked-files=no").Output()
	return strings.TrimSpace(string(out)), len(strings.TrimSpace(string(status))) > 0
}

// executeRun runs r over ids and records the final run status; an
// interrupted run stays partial and can be resumed with -resume.
func executeRun(ctx context.Context, st bench.Store, r *bench.Runner, run *bench.Run, ids []string) (bench.RunSummary, error) {
	sum, err := r.Run(ctx, run, ids)
	status := bench.RunCompleted
	switch {
	case err != nil && ctx.Err() != nil:
		status = bench.RunPartial
	case err != nil:
		status = bench.RunFailed
	}
	if ferr := st.FinishRun(context.WithoutCancel(ctx), run.ID, status, time.Now().UTC()); ferr != nil && err == nil {
		err = ferr
	}
	return sum, err
}

// agentConfig is stored in bench_runs.config for dcode runs; -resume reuses it.
type agentConfig struct {
	Limit         int     `json:"limit"`
	Tasks         int     `json:"tasks"`
	MaxSteps      int     `json:"max_steps"`
	MaxCostUSD    float64 `json:"max_cost_usd"`
	TimeoutSec    float64 `json:"timeout_sec,omitempty"`
	PruneImages   bool    `json:"prune_images"`
	KeepWorkspace bool    `json:"keep_workspace"`
}

func runRun(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("run", stderr)
	subset := fs.String("subset", bench.SubsetSmoke, "subset name (smoke50, hard, full) or ID-list file")
	limit := fs.Int("limit", 0, "only the first N tasks of the subset (0 = all)")
	model := fs.String("model", "", "model spec (default: $"+config.ModelEnvVar+", then config.toml)")
	trials := fs.Int("trials", 1, "attempts per task")
	concurrency := fs.Int("concurrency", 1, "parallel attempts (each needs 1 CPU and 4 GB)")
	maxSteps := fs.Int("max-steps", 250, "main-agent model calls per attempt")
	maxCost := fs.Float64("max-cost", 3, "USD per attempt (0 = unlimited)")
	timeout := fs.Duration("timeout", 0, "agent time per attempt (default: the task's 50 minutes)")
	resume := fs.String("resume", "", "continue an interrupted run by ID")
	retryInfra := fs.Bool("retry-infra", false, "re-run attempts that ended in infra_error")
	prune := fs.Bool("prune-images", false, "remove each task image after its last attempt")
	keep := fs.Bool("keep-workspace", false, "keep the agent's copy of each repository")
	notes := fs.String("notes", "", "free-form notes stored with the run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := bench.LoadCatalog(e.dataDir)
	if err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	docker := bench.CLIDocker{}

	var run *bench.Run
	cfg := agentConfig{Limit: *limit, MaxSteps: *maxSteps, MaxCostUSD: *maxCost, TimeoutSec: timeout.Seconds(),
		PruneImages: *prune, KeepWorkspace: *keep}
	if *resume != "" {
		if run, err = st.Run(ctx, *resume); err != nil {
			return err
		}
		if run.Solver != bench.SolverDcode {
			return fmt.Errorf("run %s used solver %s; resume it with dbench gate", run.ID, run.Solver)
		}
		if err := json.Unmarshal(run.Config, &cfg); err != nil {
			return fmt.Errorf("run %s config: %w", run.ID, err)
		}
		*subset, *model = run.Subset, run.Model
	}
	ids, err := subsetIDs(c, *subset, cfg.Limit)
	if err != nil {
		return err
	}
	spec := resolveModel(e, *model)
	if run == nil {
		cfg.Tasks = len(ids)
		if run, err = newRun(ctx, e, c, docker, bench.SolverDcode, *subset, spec, *trials, *concurrency, cfg); err != nil {
			return err
		}
		run.Notes = *notes
		if err := st.CreateRun(ctx, run); err != nil {
			return err
		}
	}
	prices, err := cost.LoadTable(filepath.Join(e.paths.UserDir, "prices.json"))
	if err != nil {
		return err
	}
	solver := &bench.DcodeSolver{
		Settings: e.settings, Prices: prices, MaxSteps: cfg.MaxSteps, MaxCostUSD: cfg.MaxCostUSD,
		Timeout: time.Duration(cfg.TimeoutSec * float64(time.Second)), KeepWorkspace: cfg.KeepWorkspace,
		NewModel: func() (llm.ChatModel, error) { return llm.InitChatModel(spec, modelOptions(e)) },
	}
	if _, err := solver.NewModel(); err != nil {
		return fmt.Errorf("init model %q: %w", spec, err)
	}
	fmt.Fprintf(stdout, "run %s: model %s, %d tasks x %d trials\n", run.ID, spec, len(ids), run.Trials)
	r := &bench.Runner{Store: st, Docker: docker, Catalog: c, Solver: solver, LogRoot: filepath.Join(e.dataDir, "logs"),
		Concurrency: max(run.Concurrency, 1), RetryInfra: *retryInfra, PruneImages: cfg.PruneImages, Progress: stdout}
	sum, err := executeRun(ctx, st, r, run, ids)
	fmt.Fprintf(stdout, "run %s: %d/%d resolved, %d infra errors\n", run.ID, sum.Resolved, sum.Total, sum.Infra)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintf(stdout, "interrupted; continue with: dbench run -resume %s\n", run.ID)
		}
		return err
	}
	fmt.Fprintf(stdout, "report: dbench report %s\n", run.ID)
	return nil
}

func runGrade(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("grade", stderr)
	concurrency := fs.Int("concurrency", 1, "parallel graders")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: dbench grade [flags] <run-id>")
	}
	c, err := bench.LoadCatalog(e.dataDir)
	if err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	run, err := st.Run(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	var cfg agentConfig
	_ = json.Unmarshal(run.Config, &cfg)
	ids, err := subsetIDs(c, run.Subset, cfg.Limit)
	if err != nil {
		return err
	}
	n, err := bench.ResetForRegrade(ctx, st, run.ID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "run %s: re-grading %d attempts\n", run.ID, n)
	var solver bench.Solver = bench.NopSolver{}
	if run.Solver != bench.SolverNop {
		solver = regradeOnly{name: run.Solver}
	}
	r := &bench.Runner{Store: st, Docker: bench.CLIDocker{}, Catalog: c, Solver: solver,
		LogRoot: filepath.Join(e.dataDir, "logs"), Concurrency: *concurrency, Progress: stdout}
	sum, err := executeRun(ctx, st, r, run, ids)
	fmt.Fprintf(stdout, "run %s: %d/%d resolved, %d infra errors\n", run.ID, sum.Resolved, sum.Total, sum.Infra)
	return err
}

// regradeOnly stands in for the run's solver while re-grading; attempts
// without a stored patch end as infra errors instead of re-running an agent.
type regradeOnly struct{ name string }

func (s regradeOnly) Name() string { return s.name }

func (regradeOnly) Solve(context.Context, bench.Task, bench.SolveEnv) (bench.SolveResult, error) {
	return bench.SolveResult{}, errors.New("no stored patch to re-grade; resume the run instead")
}

func resolveModel(e env, flagSpec string) string {
	switch {
	case flagSpec != "":
		return flagSpec
	case os.Getenv(config.ModelEnvVar) != "":
		return os.Getenv(config.ModelEnvVar)
	case e.settings != nil && e.settings.Model != "":
		return e.settings.Model
	}
	return server.DefaultModelSpec
}

func modelOptions(e env) map[string]any {
	if e.settings == nil || e.settings.MaxRetries == nil {
		return nil
	}
	return map[string]any{"max_retries": *e.settings.MaxRetries}
}

func runGate(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("gate", stderr)
	subset := fs.String("subset", bench.SubsetSmoke, "subset name (smoke50, hard, full) or ID-list file")
	limit := fs.Int("limit", 0, "only the first N tasks of the subset (0 = all)")
	concurrency := fs.Int("concurrency", 1, "parallel graders")
	solvers := fs.String("solvers", "oracle,nop", "comma-separated gate solvers")
	prune := fs.Bool("prune-images", false, "remove each task image after grading")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := bench.LoadCatalog(e.dataDir)
	if err != nil {
		return err
	}
	ids, err := subsetIDs(c, *subset, *limit)
	if err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	docker := bench.CLIDocker{}

	var verdicts []string
	ok := true
	for _, name := range strings.Split(*solvers, ",") {
		var solver bench.Solver
		want := 0
		switch strings.TrimSpace(name) {
		case bench.SolverOracle:
			solver, want = bench.OracleSolver{}, len(ids)
		case bench.SolverNop:
			solver = bench.NopSolver{}
		default:
			return fmt.Errorf("gate solver must be oracle or nop, got %q", name)
		}
		run, err := newRun(ctx, e, c, docker, solver.Name(), *subset, "", 1, *concurrency,
			map[string]any{"limit": *limit, "tasks": len(ids)})
		if err != nil {
			return err
		}
		if err := st.CreateRun(ctx, run); err != nil {
			return err
		}
		r := &bench.Runner{Store: st, Docker: docker, Catalog: c, Solver: solver,
			LogRoot: filepath.Join(e.dataDir, "logs"), Concurrency: *concurrency, PruneImages: *prune, Progress: stdout}
		sum, err := executeRun(ctx, st, r, run, ids)
		if err != nil {
			return fmt.Errorf("run %s: %w", run.ID, err)
		}
		pass := sum.Resolved == want && sum.Infra == 0
		ok = ok && pass
		verdicts = append(verdicts, fmt.Sprintf("%s %d/%d resolved (want %d, infra errors %d) run=%s",
			solver.Name(), sum.Resolved, sum.Total, want, sum.Infra, run.ID))
	}
	for _, v := range verdicts {
		fmt.Fprintln(stdout, "gate:", v)
	}
	if !ok {
		return fmt.Errorf("gate failed; inspect with: dbench report <run>")
	}
	fmt.Fprintln(stdout, "gate: PASS")
	return nil
}
