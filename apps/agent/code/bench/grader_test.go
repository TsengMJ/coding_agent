package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeDocker simulates containers: verify decides what the verifier
// reports for the patch that was applied.
type fakeDocker struct {
	mu        sync.Mutex
	applyExit int
	// verify returns reward and per-test statuses for the applied patch
	// ("" when the container got no patch).
	verify     func(patch string) (reward string, tests map[string]string)
	timeout    bool
	imageErr   error
	runErr     error
	patches    map[string]string
	logsDir    map[string]bool
	calls      []string
	removed    int
	nextID     int
	runSpecs   []ContainerSpec
	execHook   func(id string, spec ExecSpec) (ExecResult, bool)
	copyToHook func(id, src, dst string) error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{patches: map[string]string{}, logsDir: map[string]bool{}, verify: func(string) (string, map[string]string) { return "0", nil }}
}

func (f *fakeDocker) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeDocker) EnsureImage(_ context.Context, image string) (string, error) {
	f.record("image " + image)
	if f.imageErr != nil {
		return "", f.imageErr
	}
	return image + "@sha256:abc", nil
}

func (f *fakeDocker) Run(_ context.Context, spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return "", f.runErr
	}
	f.nextID++
	f.runSpecs = append(f.runSpecs, spec)
	id := fmt.Sprintf("c%d", f.nextID)
	f.calls = append(f.calls, "run "+id)
	return id, nil
}

func (f *fakeDocker) Exec(_ context.Context, id string, spec ExecSpec) (ExecResult, error) {
	cmd := strings.Join(spec.Cmd, " ")
	f.record("exec " + id + " " + cmd)
	if f.execHook != nil {
		if res, ok := f.execHook(id, spec); ok {
			return res, nil
		}
	}
	switch {
	case cmd == "mkdir -p /logs/verifier":
		f.mu.Lock()
		f.logsDir[id] = true
		f.mu.Unlock()
		return ExecResult{}, nil
	case strings.Contains(cmd, "git apply"):
		return ExecResult{ExitCode: f.applyExit}, nil
	case cmd == "bash /tests/test.sh":
		if f.timeout {
			return ExecResult{ExitCode: 124, TimedOut: true}, nil
		}
		return ExecResult{Stdout: []byte("RESULT")}, nil
	}
	return ExecResult{}, nil
}

func (f *fakeDocker) CopyTo(_ context.Context, id, src, dst string) error {
	f.record("cp-to " + id + " " + dst)
	if f.copyToHook != nil {
		return f.copyToHook(id, src, dst)
	}
	if dst == "/tmp/replay.patch" {
		data, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		f.mu.Lock()
		f.patches[id] = string(data)
		f.mu.Unlock()
	}
	return nil
}

func (f *fakeDocker) CopyFrom(_ context.Context, id, src, dst string) error {
	f.record("cp-from " + id + " " + src)
	if src != "/logs/verifier" {
		return nil
	}
	if f.timeout {
		return errors.New("no such path")
	}
	f.mu.Lock()
	patch := f.patches[id]
	f.mu.Unlock()
	reward, tests := f.verify(patch)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dst, "reward.txt"), []byte(reward+"\n"), 0o644); err != nil {
		return err
	}
	// Like the real test.sh, output.json only lands in /logs/verifier when
	// the directory existed before the tests ran.
	f.mu.Lock()
	hasLogsDir := f.logsDir[id]
	f.mu.Unlock()
	if tests == nil || !hasLogsDir {
		return nil
	}
	var out verifierOutput
	for name, st := range tests {
		out.Tests = append(out.Tests, struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		}{name, st})
	}
	data, _ := json.Marshal(out)
	return os.WriteFile(filepath.Join(dst, "output.json"), data, 0o644)
}

func (f *fakeDocker) CopyOut(_ context.Context, image, src, dst string) error {
	f.record("cp-out " + image + " " + src)
	return nil
}

func (f *fakeDocker) Remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed++
	return nil
}

func (f *fakeDocker) RemoveImage(_ context.Context, image string) error {
	f.record("rmi " + image)
	return nil
}

func (f *fakeDocker) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func gradeTask() Task {
	return Task{InstanceID: "instance_x", Image: "img:x", Dir: "/nonexistent", FailToPass: []string{"f1", "f2", "f1"},
		PassToPass: []string{"p1"}, VerifyTimeout: 60, VerifierOnline: true, CPUs: 1, MemoryMB: 4096}
}

func TestGradeResolved(t *testing.T) {
	d := newFakeDocker()
	d.verify = func(patch string) (string, map[string]string) {
		return "1", map[string]string{"f1": "PASSED", "f2": "PASSED", "p1": "PASSED", "other": "FAILED"}
	}
	dir := t.TempDir()
	g, err := Grader{Docker: d}.Grade(context.Background(), gradeTask(), "diff --git a/x b/x\n", GradeOptions{LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Resolved || g.F2PPassed != 2 || g.F2PTotal != 2 || g.P2PPassed != 1 || g.P2PTotal != 1 || g.ApplyFailed {
		t.Fatalf("result %+v", g)
	}
	if d.removed != 1 {
		t.Fatalf("container not removed")
	}
	if spec := d.runSpecs[0]; spec.NoNetwork || spec.MemoryMB != 4096 {
		t.Fatalf("grading container spec %+v", spec)
	}
	for _, f := range []string{"model.patch", "verifier.log", "verifier/reward.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
}

func TestGradeApplyFailureStillRunsVerifier(t *testing.T) {
	d := newFakeDocker()
	d.applyExit = 1
	d.verify = func(string) (string, map[string]string) {
		return "0", map[string]string{"f1": "FAILED", "p1": "PASSED"}
	}
	g, err := Grader{Docker: d}.Grade(context.Background(), gradeTask(), "garbage", GradeOptions{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !g.ApplyFailed || g.Resolved || d.called("exec c1 bash /tests/test.sh") != 1 {
		t.Fatalf("result %+v calls %v", g, d.calls)
	}
	if got := Classify("", g); got != CategoryApplyFailed {
		t.Fatalf("category %s", got)
	}
}

func TestGradeEmptyPatchSkipsVerifierUnlessRequested(t *testing.T) {
	d := newFakeDocker()
	g, err := Grader{Docker: d}.Grade(context.Background(), gradeTask(), "  \n", GradeOptions{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !g.EmptyPatch || g.Resolved || d.called("run") != 0 || g.F2PTotal != 2 {
		t.Fatalf("result %+v calls %v", g, d.calls)
	}
	g, err = Grader{Docker: d}.Grade(context.Background(), gradeTask(), "", GradeOptions{LogDir: t.TempDir(), RunEmpty: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.called("exec c1 bash /tests/test.sh") != 1 || d.called("exec c1 sh -c") != 0 {
		t.Fatalf("nop should run the verifier without applying: %v", d.calls)
	}
	if g.Tests[0].Status != "MISSING" {
		t.Fatalf("missing output.json should give MISSING, got %+v", g.Tests)
	}
}

func TestGradeVerifierTimeout(t *testing.T) {
	d := newFakeDocker()
	d.timeout = true
	g, err := Grader{Docker: d}.Grade(context.Background(), gradeTask(), "diff", GradeOptions{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !g.VerifierTimedOut || g.Resolved || g.F2PPassed != 0 {
		t.Fatalf("result %+v", g)
	}
}

func TestGradeRewardWithoutPassingTestsIsError(t *testing.T) {
	d := newFakeDocker()
	d.verify = func(string) (string, map[string]string) { return "1", nil }
	if _, err := (Grader{Docker: d}).Grade(context.Background(), gradeTask(), "diff", GradeOptions{LogDir: t.TempDir()}); err == nil {
		t.Fatal("reward 1 with no passing tests must not count as resolved")
	}
}

func TestGradeDockerFailureIsError(t *testing.T) {
	d := newFakeDocker()
	d.runErr = errors.New("daemon down")
	if _, err := (Grader{Docker: d}).Grade(context.Background(), gradeTask(), "diff", GradeOptions{LogDir: t.TempDir()}); err == nil {
		t.Fatal("expected error")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		agent string
		g     GradeResult
		want  string
	}{
		{"", GradeResult{Resolved: true}, CategoryResolved},
		{CategoryAgentTimeout, GradeResult{Resolved: true}, CategoryResolved},
		{CategoryAgentTimeout, GradeResult{F2PTotal: 1}, CategoryAgentTimeout},
		{"", GradeResult{EmptyPatch: true, F2PTotal: 1}, CategoryEmptyPatch},
		{"", GradeResult{ApplyFailed: true}, CategoryApplyFailed},
		{"", GradeResult{F2PPassed: 1, F2PTotal: 2, P2PPassed: 1, P2PTotal: 1}, CategoryIncompleteFix},
		{"", GradeResult{F2PPassed: 2, F2PTotal: 2, P2PPassed: 0, P2PTotal: 1}, CategoryRegression},
	}
	for _, tc := range cases {
		if got := Classify(tc.agent, tc.g); got != tc.want {
			t.Errorf("Classify(%q, %+v) = %s, want %s", tc.agent, tc.g, got, tc.want)
		}
	}
}
