package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tsengmingchun/quill/code/bench"
	"github.com/tsengmingchun/quill/code/cost"
	"github.com/tsengmingchun/quill/code/trace"
)

// runAnalyze recomputes process metrics from each attempt's trace and
// classifies what went wrong, without calling a model.
func runAnalyze(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("analyze", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: quillbench analyze <run-id>...")
	}
	tasks, err := taskMap(e)
	if err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	pool, err := pgxpool.New(ctx, os.Getenv(postgresEnvVar))
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()
	pgTraces, err := trace.NewPostgresLog(ctx, pool)
	if err != nil {
		return err
	}
	prices, err := cost.LoadTable(filepath.Join(e.paths.UserDir, "prices.json"))
	if err != nil {
		return err
	}
	for _, id := range fs.Args() {
		run, err := st.Run(ctx, id)
		if err != nil {
			return err
		}
		attempts, err := st.Attempts(ctx, run.ID)
		if err != nil {
			return err
		}
		results, err := st.TestResults(ctx, run.ID)
		if err != nil {
			return err
		}
		rows := make([]bench.AttemptAnalysis, 0, len(attempts))
		for _, a := range attempts {
			calls, traceErr := loadTrace(pgTraces, a.TracePath)
			task := tasks[a.InstanceID]
			rows = append(rows, bench.AnalyzeAttempt(a, task, results[bench.AttemptKey{InstanceID: a.InstanceID, Trial: a.Trial}],
				calls, traceErr, prices))
		}
		if err := writeReport(e, "analyze-"+run.ID, bench.AnalyzeMarkdown(*run, rows), stdout); err != nil {
			return err
		}
	}
	return nil
}

// loadTrace reads an attempt's trace from Postgres or, for runs recorded
// before traces moved there, from its trace.jsonl.
func loadTrace(pg trace.Log, tracePath string) ([]trace.LLMCallDetail, error) {
	switch {
	case tracePath == "":
		return nil, errors.New("no trace recorded")
	case strings.HasPrefix(tracePath, bench.TracePrefix):
		return trace.NewStore(pg).Details(strings.TrimPrefix(tracePath, bench.TracePrefix))
	}
	return trace.NewStore(trace.FileLog{Path: tracePath}).Details("")
}
