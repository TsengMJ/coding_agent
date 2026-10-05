package codetools

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/tool"
)

func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
}

// goModule writes a small module with a passing test, a failing test and,
// optionally, a compile error.
func goModule(t *testing.T, broken bool) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/m\n\ngo 1.21\n")
	writeFile(t, dir, "ws/client.go", "package ws\n\nfunc Attempts() int { return 1 }\n")
	writeFile(t, dir, "ws/client_test.go", `package ws

import "testing"

func TestOK(t *testing.T) { t.Log("noise from a passing test") }

func TestReconnect(t *testing.T) {
	if got := Attempts(); got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}
`)
	if broken {
		writeFile(t, dir, "bad/bad.go", "package bad\n\nfunc F() int { return retryPolicy }\n")
	}
	return dir
}

func TestRunTestsEndToEnd(t *testing.T) {
	needGo(t)
	dir := goModule(t, true)
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), nil))
	if rep.Status != "failed" || rep.Passed != 1 || rep.Failed != 1 {
		t.Fatalf("report = %+v", rep)
	}
	f := rep.Failures[0]
	if f.Test != "TestReconnect" || f.File != "client_test.go" || f.Line != 9 || f.Message != "expected 3 attempts, got 1" {
		t.Errorf("failure = %+v", f)
	}
	if len(rep.BuildErrors) != 1 || rep.BuildErrors[0].File != "bad/bad.go" || rep.BuildErrors[0].Line != 3 || !strings.Contains(rep.BuildErrors[0].Message, "retryPolicy") {
		t.Errorf("build errors = %+v", rep.BuildErrors)
	}
}

func TestRunTestsRunFilterAndPackages(t *testing.T) {
	needGo(t)
	dir := goModule(t, true)
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, dir)), map[string]any{"packages": []any{"./ws"}, "run": "^TestOK$"}))
	if rep.Status != "passed" || rep.Passed != 1 || rep.Failed != 0 || len(rep.BuildErrors) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestDiagnosticsEndToEnd(t *testing.T) {
	needGo(t)
	dir := goModule(t, true)
	res := invoke(t, NewDiagnostics(newShell(t, dir)), nil)
	diags := decode[[]Diagnostic](t, res)
	if len(diags) != 1 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	d := diags[0]
	if d.File != "bad/bad.go" || d.Line != 3 || d.Column != 23 || d.Source != "build" || d.Message != "undefined: retryPolicy" {
		t.Fatalf("diagnostic = %+v", d)
	}

	clean := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, goModule(t, false))), nil))
	if len(clean) != 0 {
		t.Fatalf("clean module diagnostics = %+v", clean)
	}
}

func TestDiagnosticsVetFindings(t *testing.T) {
	needGo(t)
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/v\n\ngo 1.21\n")
	writeFile(t, dir, "v.go", "package v\n\nimport \"fmt\"\n\nfunc F() { fmt.Printf(\"%d\", \"s\") }\n")
	diags := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), nil))
	if len(diags) != 1 || diags[0].Source != "vet" || diags[0].File != "v.go" || diags[0].Line != 5 {
		t.Fatalf("diagnostics = %+v", diags)
	}
	none := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, dir)), map[string]any{"vet": false}))
	if len(none) != 0 {
		t.Fatalf("vet=false diagnostics = %+v", none)
	}
}

func TestGoToolsUnsupportedWithoutModule(t *testing.T) {
	needGo(t)
	dir := t.TempDir()
	t.Setenv("GOFLAGS", "")
	t.Setenv("GO111MODULE", "")
	t.Setenv("GOWORK", "off")
	for _, tl := range []tool.Tool{NewRunTests(newShell(t, dir)), NewDiagnostics(newShell(t, dir))} {
		res := invoke(t, tl, nil)
		var u Unsupported
		if err := json.Unmarshal([]byte(res.Content[0].Text), &u); err != nil || u.Status != "unsupported" || !strings.Contains(u.Reason, "not a Go module") {
			t.Fatalf("%s: %s", tl.Name(), res.Content[0].Text)
		}
	}
}

func TestGoPackagesValidation(t *testing.T) {
	for _, bad := range []string{"-exec=sh", "./x; rm -rf /", "../other/...", "a b", "$(id)"} {
		if _, err := goPackages(map[string]any{"packages": []any{bad}}); err == nil {
			t.Errorf("package %q accepted", bad)
		}
	}
	for _, good := range []string{"./...", "./internal/ws", "example.com/m/ws/...", "."} {
		if _, err := goPackages(map[string]any{"packages": []any{good}}); err != nil {
			t.Errorf("package %q rejected: %v", good, err)
		}
	}
	if _, err := goTestArgs(map[string]any{"run": "a\nb"}); err == nil {
		t.Error("multi-line run pattern accepted")
	}
	if got, _ := goTestArgs(map[string]any{"run": "Test'X"}); got != `-run 'Test'\''X' ./...` {
		t.Errorf("run quoting = %s", got)
	}
}

func TestSplitExit(t *testing.T) {
	outs, codes := splitExit("a\nb\n" + exitMarker + "1\nc\n" + exitMarker + "0\n")
	if len(outs) != 2 || outs[0] != "a\nb\n" || outs[1] != "\nc\n" || codes[0] != 1 || codes[1] != 0 {
		t.Fatalf("outs=%q codes=%v", outs, codes)
	}
}
