package codetools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
)

func TestSyntaxCheckPythonAndJavaScript(t *testing.T) {
	needCmd(t, "python3")
	needCmd(t, "node")
	dir := t.TempDir()
	writeFile(t, dir, "app/ok.py", "x = 1\n")
	writeFile(t, dir, "app/bad.py", "def f(:\n    pass\n")
	writeFile(t, dir, "web/ok.js", "module.exports = 1;\n")
	writeFile(t, dir, "web/bad.js", "function f() {\n  return 1;\n}\n}\n")
	writeFile(t, dir, "web/types.ts", "export const n: number = ;\n")
	got, err := SyntaxCheck(context.Background(), newShell(t, dir), []string{"app/ok.py", "app/bad.py", "web/ok.js", "web/bad.js", "web/types.ts", "README.md"}, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("diagnostics = %+v", got)
	}
	if got[0].File != "app/bad.py" || got[0].Line != 1 || !strings.HasPrefix(got[0].Message, "SyntaxError") {
		t.Errorf("python = %+v", got[0])
	}
	if got[1].File != "web/bad.js" || got[1].Line != 4 || got[1].Severity != "error" {
		t.Errorf("javascript = %+v", got[1])
	}
}

func TestSyntaxCheckWithoutInterpreterIsEmpty(t *testing.T) {
	sb := scripted{out: map[string]string{"command -v": missingMarker + "\n" + exitMarker + "0"}}
	got, err := SyntaxCheck(context.Background(), sb, []string{"a.py"}, time.Second)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDiagnoseFilesReportsOnlyTheGivenGoFiles(t *testing.T) {
	needCmd(t, "go")
	dir := t.TempDir()
	writeFile(t, dir, "svc/go.mod", "module m\n\ngo 1.21\n")
	writeFile(t, dir, "svc/a/a.go", "package a\n\nfunc A() int { return missing }\n")
	writeFile(t, dir, "svc/b/b.go", "package b\n\nfunc B() int { return alsoMissing }\n")
	got, err := DiagnoseFiles(context.Background(), newShell(t, dir), []string{"svc/a/a.go"}, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].File != "svc/a/a.go" || got[0].Line != 3 || !strings.Contains(got[0].Message, "undefined: missing") {
		t.Fatalf("diagnostics = %+v", got)
	}
}

func TestDiagnoseFilesCleanPackage(t *testing.T) {
	needCmd(t, "go")
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module m\n\ngo 1.21\n")
	writeFile(t, dir, "a.go", "package m\n\nfunc A() int { return 1 }\n")
	got, err := DiagnoseFiles(context.Background(), newShell(t, dir), []string{"a.go"}, 60*time.Second)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestDiagnoseFilesErrorsWhenTheCheckDoesNotFinish(t *testing.T) {
	sb := scripted{out: map[string]string{}}
	if _, err := DiagnoseFiles(context.Background(), sb, []string{"a.go"}, time.Second); err == nil {
		t.Fatal("want an error when no command reports an exit code")
	}
}

// scripted answers Execute with the output of the first key the command
// contains, and with no exit code (as on a timeout) otherwise.
type scripted struct {
	backend.SandboxBackend
	out map[string]string
}

func (s scripted) ID() string { return "scripted" }

func (s scripted) Execute(_ context.Context, command string, _ *time.Duration) backend.ExecuteResponse {
	for k, v := range s.out {
		if strings.Contains(command, k) {
			code := 0
			return backend.ExecuteResponse{Output: v, ExitCode: &code}
		}
	}
	return backend.ExecuteResponse{}
}
