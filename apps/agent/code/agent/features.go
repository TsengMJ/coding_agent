package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
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
	// FeatureTestEdits runs the original version of existing tests the
	// agent changed against its code before a headless turn finishes.
	FeatureTestEdits = "test_edits"
	// FeatureProgress refuses an unchanged repeat of a call that failed
	// the same way twice (ProgressMonitor).
	FeatureProgress = "progress"
	// FeatureBudget warns a headless run to wrap up at 80% of its deadline
	// or step limit.
	FeatureBudget = "budget"
	// FeatureFailureClass appends a [repair] line to check results that
	// classifies the failure and compares it with the previous run after
	// an edit (FailureClassifier).
	FeatureFailureClass = "failure_class"
	// FeatureRepairState offers the repair tool and adds the repair state
	// (task, hypothesis, baseline, attempts, phase) to each request
	// (RepairTracker).
	FeatureRepairState = "repair_state"
	// FeatureMicro checks the files an edit changed right away: a new
	// syntax error rolls the edit back, compile / lint errors are appended
	// (MicroCheck).
	FeatureMicro = "micro"
	// FeatureRepro notes, on the first code edit of a headless turn, that
	// no failing check has reproduced the problem yet (ReproReminder).
	FeatureRepro = "repro"
	// FeaturePlanner plans which tests a change needs (the changed files'
	// tests, then their dependents, then build and lint), shows it in the
	// repair state and has the headless gate widen verification once
	// (VerificationPlanner).
	FeaturePlanner = "planner"
	// FeatureStuck notices attempts that go in circles, asks for a new
	// strategy and, headless, refuses the next edit once until the
	// hypothesis changes (StuckDetector).
	FeatureStuck = "stuck"
	// FeatureCheckpoint saves the workspace in git refs before the first
	// edit of a turn and after each check an edit improved, and offers the
	// rollback tool (CheckpointManager).
	FeatureCheckpoint = "checkpoint"
	// FeatureReviewer has an independent read-only reviewer check a
	// verified headless change once (Options.ReviewModel).
	FeatureReviewer = "reviewer"
)

// Features lists every name Options.Disable accepts.
var Features = []string{FeatureBaseline, FeatureDiffReview, FeatureTestEdits, FeatureProgress, FeatureBudget, FeatureFailureClass, FeatureRepairState, FeatureMicro, FeatureRepro, FeaturePlanner, FeatureStuck, FeatureCheckpoint, FeatureReviewer}

// DefaultDisabled is what quill ships with: the verify gate, micro checks,
// checkpoints and the progress / budget guards stay on; the rest of the
// repair loop multiplied steps and cost in the RL8 ablation without solving
// more tasks.
var DefaultDisabled = []string{FeatureFailureClass, FeatureRepairState, FeatureRepro, FeaturePlanner, FeatureStuck, FeatureReviewer}

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

// newVerifyGate builds the headless gate; the baseline, the test-edit check
// and the diff review need a workspace that runs commands.
func newVerifyGate(b backend.Backend, disable []string) VerifyGate {
	sb, ok := b.(backend.SandboxBackend)
	if !ok {
		return VerifyGate{}
	}
	g := VerifyGate{Workspace: sb, RunTests: codetools.NewRunTests(sb)}
	g.NoBaseline = slices.Contains(disable, FeatureBaseline)
	g.NoDiffReview = slices.Contains(disable, FeatureDiffReview)
	g.NoTestEdits = slices.Contains(disable, FeatureTestEdits)
	return g
}

// newPlanner returns the verification planner, or nil when b cannot run
// commands or the planner is disabled.
func newPlanner(b backend.Backend, disable []string) *VerificationPlanner {
	sb, ok := b.(backend.SandboxBackend)
	if !ok || slices.Contains(disable, FeaturePlanner) {
		return nil
	}
	return NewVerificationPlanner(sb)
}
