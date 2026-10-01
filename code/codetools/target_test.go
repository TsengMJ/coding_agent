package codetools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelativeTo(t *testing.T) {
	got, err := relativeTo([]string{"web/test/a.js", "web"}, "web")
	if err != nil || got[0] != "test/a.js" || got[1] != "." {
		t.Fatalf("relativeTo = %v, %v", got, err)
	}
	if _, err := relativeTo([]string{"webapp/a.js"}, "web"); err == nil {
		t.Error("a file outside dir was accepted")
	}
}

// The project directory comes from the ancestors of "files"; the runner runs
// there and failure paths come back relative to the workspace root.
func TestRunTestsMochaFindsProjectAboveFiles(t *testing.T) {
	needCmd(t, "npx")
	root := realDir(t)
	dir := filepath.Join(root, "web")
	writeFile(t, root, "README.md", "monorepo\n")
	writeFile(t, dir, "package.json", `{"scripts":{"test":"mocha"},"devDependencies":{"mocha":"10"}}`)
	writeFile(t, dir, "report.json", cwdFixture(t, "mocha_report.txt", dir))
	writeFile(t, dir, "node_modules/.bin/mocha", "#!/bin/sh\necho \"$@\" > args.txt\ncat report.json\nexit 1\n")
	if err := os.Chmod(filepath.Join(dir, "node_modules/.bin/mocha"), 0o755); err != nil {
		t.Fatal(err)
	}
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, root)), map[string]any{"files": []any{"web/test/user.js"}}))
	if rep.Framework != frameworkMocha || len(rep.Failures) != 1 || rep.Failures[0].File != "web/test/user.js" {
		t.Fatalf("report = %+v", rep)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if got := strings.TrimSpace(string(args)); got != "--reporter json --exit test/user.js" {
		t.Errorf("mocha args = %q", got)
	}
}

func TestRunTestsPytestWithDir(t *testing.T) {
	needCmd(t, "python3")
	root := realDir(t)
	dir := filepath.Join(root, "api")
	writeFile(t, root, "pyproject.toml", "[project]\nname = 'root'\n")
	writeFile(t, dir, "pyproject.toml", "[project]\nname = 'x'\n")
	writeFile(t, dir, "report.xml", cwdFixture(t, "pytest_report.xml", dir))
	writeFile(t, dir, "pytest/__init__.py", "")
	writeFile(t, dir, "pytest/__main__.py", `import shutil, sys
args = sys.argv[1:]
shutil.copy("report.xml", next(a.split("=", 1)[1] for a in args if a.startswith("--junitxml=")))
open("args.txt", "w").write(" ".join(a for a in args if not a.startswith("--junitxml=")))
sys.exit(1)
`)
	rep := decode[TestReport](t, invoke(t, NewRunTests(newShell(t, root)), map[string]any{"dir": "api", "files": []any{"api/tests/test_api.py"}}))
	if rep.Framework != frameworkPytest || rep.Failed != 2 || rep.Failures[0].File != "api/tests/test_api.py" {
		t.Fatalf("report = %+v", rep)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args.txt"))
	if got := string(args); got != "-q -p no:cacheprovider tests/test_api.py" {
		t.Errorf("pytest args = %q", got)
	}
}

func TestDiagnosticsFindsOnlySubproject(t *testing.T) {
	needGo(t)
	root := t.TempDir()
	writeFile(t, root, "svc/go.mod", "module example.com/svc\n\ngo 1.21\n")
	writeFile(t, root, "svc/bad/bad.go", "package bad\n\nfunc F() int { return retryPolicy }\n")
	writeFile(t, root, "node_modules/x/go.mod", "module example.com/ignored\n")
	diags := decode[[]Diagnostic](t, invoke(t, NewDiagnostics(newShell(t, root)), map[string]any{"language": "go"}))
	if len(diags) != 1 || diags[0].File != "svc/bad/bad.go" || diags[0].Line != 3 {
		t.Fatalf("diagnostics = %+v", diags)
	}
}

func TestRunTestsSeveralSubprojects(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "web/package.json", `{"name":"web"}`)
	writeFile(t, root, "admin/package.json", `{"name":"admin"}`)
	u := decode[Unsupported](t, invoke(t, NewRunTests(newShell(t, root)), nil))
	if u.Status != "unsupported" || !strings.Contains(u.Reason, "admin, web") || !strings.Contains(u.Hint, "dir") {
		t.Fatalf("result = %+v", u)
	}
}

func TestRunTestsRejectsBadDir(t *testing.T) {
	res := invoke(t, NewRunTests(newShell(t, t.TempDir())), map[string]any{"dir": "../elsewhere"})
	if !res.IsError {
		t.Fatalf("result = %s", res.Content[0].Text)
	}
}
