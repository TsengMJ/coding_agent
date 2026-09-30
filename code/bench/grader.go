package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// applyCommand is PatchReplayAgent's apply chain (v2/tooling/patch_replay.py).
const applyCommand = "cd /app 2>/dev/null || cd /testbed; git apply --verbose /tmp/replay.patch " +
	"|| git apply --3way /tmp/replay.patch || patch --fuzz=3 -p1 -i /tmp/replay.patch"

// repoDirCommand prints the repository directory (/app, or /testbed for a
// few tasks).
const repoDirCommand = "if [ -d /app ]; then echo /app; else echo /testbed; fi"

// GradeResult is the outcome of re-grading a patch on a pristine image.
type GradeResult struct {
	Resolved    bool
	EmptyPatch  bool
	ApplyFailed bool
	F2PPassed   int
	F2PTotal    int
	P2PPassed   int
	P2PTotal    int
	Tests       []TestResult
	ApplyOutput string
	// VerifierTimedOut means tests/test.sh exceeded the task's verifier timeout.
	VerifierTimedOut bool
	Duration         time.Duration
}

// Grader re-grades patches with the task's own verifier (tests/test.sh) in a
// fresh container, as the locked protocol's PatchReplayAgent does.
type Grader struct {
	Docker Docker
}

// GradeOptions controls one grading.
type GradeOptions struct {
	// LogDir receives model.patch, verifier.log and the verifier/ directory.
	LogDir string
	// RunEmpty runs the verifier even for an empty patch (the nop gate);
	// otherwise an empty patch is unresolved without running tests.
	RunEmpty bool
}

// Grade applies patch to a pristine container of task's image and runs the
// verifier. Errors are infrastructure failures (docker, missing files); a
// failing patch is reported in the result, not as an error.
func (g Grader) Grade(ctx context.Context, task Task, patch string, opts GradeOptions) (GradeResult, error) {
	start := time.Now()
	res := GradeResult{}
	res.EmptyPatch = strings.TrimSpace(patch) == ""
	if res.EmptyPatch && !opts.RunEmpty {
		res.tally(scoreTests(task, nil))
		res.Duration = time.Since(start)
		return res, nil
	}
	if err := os.MkdirAll(opts.LogDir, 0o755); err != nil {
		return res, err
	}
	id, err := g.Docker.Run(ctx, ContainerSpec{Image: task.Image, NoNetwork: !task.VerifierOnline,
		CPUs: task.CPUs, MemoryMB: task.MemoryMB})
	if err != nil {
		return res, fmt.Errorf("start grading container: %w", err)
	}
	defer g.Docker.Remove(context.WithoutCancel(ctx), id)

	if err := g.Docker.CopyTo(ctx, id, task.TestsDir(), "/tests"); err != nil {
		return res, fmt.Errorf("copy tests: %w", err)
	}
	if !res.EmptyPatch {
		patchFile := filepath.Join(opts.LogDir, "model.patch")
		if err := os.WriteFile(patchFile, []byte(patch), 0o644); err != nil {
			return res, err
		}
		if err := g.Docker.CopyTo(ctx, id, patchFile, "/tmp/replay.patch"); err != nil {
			return res, fmt.Errorf("copy patch: %w", err)
		}
		ar, err := g.Docker.Exec(ctx, id, ExecSpec{Cmd: []string{"sh", "-c", applyCommand}, Timeout: 5 * time.Minute})
		if err != nil {
			return res, fmt.Errorf("apply patch: %w", err)
		}
		res.ApplyOutput = clipTail(string(ar.Stdout)+string(ar.Stderr), 6000)
		res.ApplyFailed = ar.ExitCode != 0
	}
	timeout := time.Duration(task.VerifyTimeout * float64(time.Second))
	vr, err := g.Docker.Exec(ctx, id, ExecSpec{Cmd: []string{"bash", "/tests/test.sh"}, Timeout: timeout})
	if err != nil {
		return res, fmt.Errorf("run verifier: %w", err)
	}
	res.VerifierTimedOut = vr.TimedOut
	log := fmt.Sprintf("exit %d\n--- stdout\n%s\n--- stderr\n%s\n--- apply\n%s\n", vr.ExitCode, vr.Stdout, vr.Stderr, res.ApplyOutput)
	if err := os.WriteFile(filepath.Join(opts.LogDir, "verifier.log"), []byte(log), 0o644); err != nil {
		return res, err
	}
	verifierDir := filepath.Join(opts.LogDir, "verifier")
	_ = os.RemoveAll(verifierDir)
	if err := g.Docker.CopyFrom(ctx, id, "/logs/verifier", verifierDir); err != nil {
		if vr.TimedOut {
			res.tally(scoreTests(task, nil))
			res.Duration = time.Since(start)
			return res, nil
		}
		return res, fmt.Errorf("collect verifier logs: %w", err)
	}
	reward, err := os.ReadFile(filepath.Join(verifierDir, "reward.txt"))
	if err != nil && !vr.TimedOut {
		return res, fmt.Errorf("verifier wrote no reward: %w", err)
	}
	res.Resolved = strings.TrimSpace(string(reward)) == "1"
	out, err := readVerifierOutput(filepath.Join(verifierDir, "output.json"))
	if err != nil {
		return res, err
	}
	res.tally(scoreTests(task, out))
	res.Duration = time.Since(start)
	return res, nil
}

// tally sets Tests and the F2P / P2P counts from them.
func (r *GradeResult) tally(tests []TestResult) {
	r.Tests = tests
	r.F2PPassed, r.F2PTotal, r.P2PPassed, r.P2PTotal = 0, 0, 0, 0
	for _, t := range tests {
		passed := 0
		if t.Status == "PASSED" {
			passed = 1
		}
		if t.Kind == "f2p" {
			r.F2PTotal++
			r.F2PPassed += passed
		} else {
			r.P2PTotal++
			r.P2PPassed += passed
		}
	}
}

type verifierOutput struct {
	Tests []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"tests"`
}

// readVerifierOutput reads parser.py's output.json; a missing file (the
// parser failed or tests never ran) yields no entries.
func readVerifierOutput(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out verifierOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("verifier output.json: %w", err)
	}
	status := map[string]string{}
	for _, t := range out.Tests {
		// test.sh counts a test as passed if any entry with its name passed.
		if status[t.Name] != "PASSED" {
			status[t.Name] = t.Status
		}
	}
	return status, nil
}

// scoreTests lists every required test with its status (MISSING when the
// parser did not report it).
func scoreTests(task Task, status map[string]string) []TestResult {
	var out []TestResult
	add := func(names []string, kind string) {
		seen := map[string]bool{}
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			st := status[n]
			if st == "" {
				st = "MISSING"
			}
			out = append(out, TestResult{Name: n, Kind: kind, Status: st})
		}
	}
	add(task.FailToPass, "f2p")
	add(task.PassToPass, "p2p")
	return out
}

func clipTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
