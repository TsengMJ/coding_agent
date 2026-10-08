package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/tsengmingchun/quill/apps/agent/code/bench"
)

func taskMap(e env) (map[string]bench.Task, error) {
	c, err := bench.LoadCatalog(e.dataDir)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bench.Task, len(c.Tasks))
	for _, t := range c.Tasks {
		m[t.InstanceID] = t
	}
	return m, nil
}

// writeReport prints md and saves it under <data>/reports.
func writeReport(e env, name, md string, stdout io.Writer) error {
	dir := filepath.Join(e.dataDir, "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Fprint(stdout, md)
	fmt.Fprintf(stdout, "\nsaved %s\n", path)
	return nil
}

func runReport(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("report", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: quillbench report <run-id>")
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
	run, err := st.Run(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	attempts, err := st.Attempts(ctx, run.ID)
	if err != nil {
		return err
	}
	return writeReport(e, run.ID, bench.RenderReport(run, bench.Summarize(attempts, tasks)), stdout)
}

func runCompare(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("compare", stderr)
	ref := fs.String("ref", "", "compare the run with imported reference grades: <harness>/<model>")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	outcomes := func(runID string) (bench.TaskOutcome, error) {
		if _, err := st.Run(ctx, runID); err != nil {
			return nil, err
		}
		attempts, err := st.Attempts(ctx, runID)
		return bench.Outcomes(attempts), err
	}
	var nameA, nameB string
	var a, b bench.TaskOutcome
	switch {
	case *ref != "" && fs.NArg() == 1:
		harness, model, ok := strings.Cut(*ref, "/")
		if !ok {
			return errors.New("-ref must be <harness>/<model>")
		}
		refs, err := st.References(ctx, harness, model)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			return fmt.Errorf("no references for %s; import them with quillbench refs import", *ref)
		}
		nameA, a = *ref, bench.ReferenceOutcomes(refs)
		nameB = fs.Arg(0)
		if b, err = outcomes(nameB); err != nil {
			return err
		}
	case *ref == "" && fs.NArg() == 2:
		nameA, nameB = fs.Arg(0), fs.Arg(1)
		if a, err = outcomes(nameA); err != nil {
			return err
		}
		if b, err = outcomes(nameB); err != nil {
			return err
		}
	default:
		return errors.New("usage: quillbench compare <base-run> <new-run> | quillbench compare -ref <harness>/<model> <run>")
	}
	c := bench.Compare(a, b)
	if c.Shared == 0 {
		return fmt.Errorf("%s and %s share no scored tasks", nameA, nameB)
	}
	name := "compare-" + strings.ReplaceAll(nameA, "/", "_") + "-vs-" + nameB
	return writeReport(e, name, bench.RenderComparison(nameA, nameB, c), stdout)
}

func runHistory(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	fs := newFlagSet("history", stderr)
	subset := fs.String("subset", "", "only runs on this subset")
	model := fs.String("model", "", "only runs with this model")
	solver := fs.String("solver", "", "only runs with this solver")
	n := fs.Int("n", 20, "number of runs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	runs, err := st.Runs(ctx, bench.RunFilter{Subset: *subset, Model: *model, Solver: *solver, Limit: *n})
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RUN\tSTATUS\tMODEL\tCOMMIT\tRESOLVED\t95% CI\tINFRA\tNOTES")
	for _, r := range runs {
		attempts, err := st.Attempts(ctx, r.ID)
		if err != nil {
			return err
		}
		s := bench.Summarize(attempts, nil)
		commit := r.GitCommit
		if len(commit) > 8 {
			commit = commit[:8]
		}
		if r.GitDirty {
			commit += "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d/%d %.1f%%\t[%.1f, %.1f]\t%d\t%s\n", r.ID, r.Status, dash(r.Model), dash(commit),
			s.Overall.Resolved, s.Overall.Total, s.Overall.Pct(), 100*s.Overall.Lo, 100*s.Overall.Hi, s.InfraError, r.Notes)
	}
	return tw.Flush()
}

func runRefs(ctx context.Context, e env, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "import" && args[0] != "list") {
		return errors.New("usage: quillbench refs import -harness H -model M [-source S] <file> | quillbench refs list")
	}
	st, closeDB, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer closeDB()
	if args[0] == "list" {
		sets, err := st.ReferenceSets(ctx)
		if err != nil {
			return err
		}
		for _, s := range sets {
			refs, err := st.References(ctx, s[0], s[1])
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s/%s: %s\n", s[0], s[1], bench.Summarize(refAttempts(refs), nil).Overall)
		}
		return nil
	}
	fs := newFlagSet("refs import", stderr)
	harness := fs.String("harness", "", "agent harness, e.g. mini-swe-agent")
	model := fs.String("model", "", "model name, e.g. glm-5.3")
	source := fs.String("source", "", "where the grades came from (URL or paper)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: quillbench refs import -harness H -model M [-source S] <file>")
	}
	data, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	src := *source
	if src == "" {
		src = filepath.Base(fs.Arg(0))
	}
	refs, err := bench.ParseReferences(data, *harness, *model, src)
	if err != nil {
		return err
	}
	tasks, err := taskMap(e)
	if err != nil {
		return err
	}
	var known []bench.Reference
	for _, r := range refs {
		if _, ok := tasks[r.InstanceID]; ok {
			known = append(known, r)
		}
	}
	if len(known) == 0 {
		return errors.New("none of the references match a V2 task ID")
	}
	n, err := st.ImportReferences(ctx, known)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "imported %d references for %s/%s (%d skipped: not V2 task IDs)\n", n, *harness, *model, len(refs)-len(known))
	return nil
}

// refAttempts lets references reuse Summarize.
func refAttempts(refs []bench.Reference) []bench.Attempt {
	out := make([]bench.Attempt, len(refs))
	for i, r := range refs {
		out[i] = bench.Attempt{InstanceID: r.InstanceID, Phase: bench.PhaseDone, Resolved: r.Resolved}
	}
	return out
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
