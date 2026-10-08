// Command quillbench evaluates quill on SWE-bench Pro V2 and records every run
// in PostgreSQL ($POSTGRES_CONN_STRING).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tsengmingchun/quill/apps/agent/code/bench"
	"github.com/tsengmingchun/quill/apps/agent/code/config"
)

const postgresEnvVar = "POSTGRES_CONN_STRING"

const usage = `usage: quillbench <command> [flags]

commands:
  prepare   check out the pinned V2 tasks, verify SHA256SUMS, cache metadata, fill bench_tasks
  subset    print a stratified subset (used to generate subsets/smoke50.txt)
  gate      grade the reference patch (oracle) and the empty patch (nop) on a subset
  run       run quill on a subset and grade every patch
  grade     re-grade a run's captured patches
  report    summarize a run
  analyze   recompute process metrics from traces and classify failures
  compare   compare two runs, or a run with a reference harness/model
  history   list runs over time
  refs      import or list reference per-task grades
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop)
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "quillbench:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		return nil
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "prepare":
		return runPrepare(ctx, e, rest, stdout, stderr)
	case "subset":
		return runSubset(e, rest, stdout, stderr)
	case "gate":
		return runGate(ctx, e, rest, stdout, stderr)
	case "run":
		return runRun(ctx, e, rest, stdout, stderr)
	case "grade":
		return runGrade(ctx, e, rest, stdout, stderr)
	case "report":
		return runReport(ctx, e, rest, stdout, stderr)
	case "analyze":
		return runAnalyze(ctx, e, rest, stdout, stderr)
	case "compare":
		return runCompare(ctx, e, rest, stdout, stderr)
	case "history":
		return runHistory(ctx, e, rest, stdout, stderr)
	case "refs":
		return runRefs(ctx, e, rest, stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return fmt.Errorf("unknown command %q", cmd)
}

type env struct {
	cwd      string
	paths    config.Paths
	settings *config.Settings
	dataDir  string
}

func loadEnv() (env, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return env{}, err
	}
	paths, err := config.DefaultPaths(cwd)
	if err != nil {
		return env{}, err
	}
	if err := config.LoadDotEnv(paths, cwd); err != nil {
		return env{}, fmt.Errorf("load .env: %w", err)
	}
	r, err := config.NewResolver(paths)
	if err != nil {
		return env{}, err
	}
	settings, err := r.Settings()
	if err != nil {
		return env{}, err
	}
	return env{cwd: cwd, paths: paths, settings: settings, dataDir: filepath.Join(paths.UserDir, "bench")}, nil
}

// openStore connects to $POSTGRES_CONN_STRING; every bench result lives there.
func openStore(ctx context.Context) (bench.Store, func(), error) {
	conn := os.Getenv(postgresEnvVar)
	if conn == "" {
		return nil, nil, errors.New(postgresEnvVar + " is not set; quillbench records every run in PostgreSQL")
	}
	pool, err := pgxpool.New(ctx, conn)
	if err != nil {
		return nil, nil, fmt.Errorf("connect postgres: %w", err)
	}
	st, err := bench.NewPostgresStore(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, nil, err
	}
	return st, pool.Close, nil
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("quillbench "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func runPrepare(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("prepare", stderr)
	noDB := fs.Bool("no-db", false, "skip writing bench_tasks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(e.dataDir, 0o755); err != nil {
		return err
	}
	res, err := bench.Prepare(ctx, e.dataDir, bench.HFClient{})
	if err != nil {
		return err
	}
	c := res.Catalog
	fmt.Fprintf(stdout, "tasks: %d at %s (commit %s), %d checksums verified, dataset revision %s\n",
		len(c.Tasks), c.V2Dir, c.TasksRepoCommit, res.Verified, c.DatasetRevision)
	if *noDB {
		return nil
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	if err := st.UpsertTasks(ctx, c.Tasks); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "bench_tasks: %d rows upserted\n", len(c.Tasks))
	return nil
}

func runSubset(e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("subset", stderr)
	n := fs.Int("n", bench.SmokeSize, "number of tasks")
	seed := fs.Uint64("seed", bench.SmokeSeed, "sampling seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := bench.LoadCatalog(e.dataDir)
	if err != nil {
		return err
	}
	ids := bench.SelectStratified(c.Tasks, *n, *seed)
	fmt.Fprint(stdout, bench.FormatSubset(ids,
		fmt.Sprintf("SWE-bench Pro V2 stratified subset: n=%d seed=%d", *n, *seed),
		"tasks repo commit "+c.TasksRepoCommit,
		"strata: language (ts folded into js), hard share, round-robin over repos",
		"Fixed: do not regenerate; compare runs on the same list."))
	return nil
}
