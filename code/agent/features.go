package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/code/codetools"
)

// Features that Options.Disable can turn off, so a benchmark can measure
// what each one is worth.
const (
	// FeatureBaseline compares a failing check against HEAD before the
	// headless gate blocks on it.
	FeatureBaseline = "baseline"
	// FeatureDiffReview reviews byproducts, changed tests and the task's
	// requirements once before a headless turn finishes.
	FeatureDiffReview = "diff_review"
	// FeatureMasking replaces old read-only tool outputs with placeholders
	// in model requests (MaskObservations).
	FeatureMasking = "masking"
	// FeatureProgress refuses an unchanged repeat of a call that failed
	// the same way twice (ProgressMonitor).
	FeatureProgress = "progress"
	// FeatureBudget warns a headless run to wrap up at 80% of its deadline
	// or step limit.
	FeatureBudget = "budget"
)

// Features lists every name Options.Disable accepts.
var Features = []string{FeatureBaseline, FeatureDiffReview, FeatureMasking, FeatureProgress, FeatureBudget}

// ParseFeatures splits a comma-separated list of feature names and rejects
// unknown ones.
func ParseFeatures(list string) ([]string, error) {
	var out []string
	for _, f := range strings.Split(list, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out, checkFeatures(out)
}

func checkFeatures(names []string) error {
	for _, f := range names {
		if !slices.Contains(Features, f) {
			return fmt.Errorf("unknown feature %q (known: %s)", f, strings.Join(Features, ", "))
		}
	}
	return nil
}

// newVerifyGate builds the headless gate; the baseline and diff review need
// a workspace that runs commands.
func newVerifyGate(b backend.Backend, disable []string) VerifyGate {
	sb, ok := b.(backend.SandboxBackend)
	if !ok {
		return VerifyGate{}
	}
	g := VerifyGate{Workspace: sb, RunTests: codetools.NewRunTests(sb)}
	g.NoBaseline = slices.Contains(disable, FeatureBaseline)
	g.NoDiffReview = slices.Contains(disable, FeatureDiffReview)
	return g
}
