package client

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func edit(path, old, repl string) hitl.ActionRequest {
	return hitl.ActionRequest{Name: "edit_file", Args: map[string]any{"file_path": path, "old_string": old, "new_string": repl}}
}

func TestActionDiffEditUsesFileContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	d := ActionDiff(edit("/main.go", `println("hi")`, `println("hello")`), dir)
	for _, want := range []string{"--- /main.go", "+++ /main.go", `-	println("hi")`, `+	println("hello")`, " func main() {"} {
		if !strings.Contains(d, want) {
			t.Fatalf("diff missing %q:\n%s", want, d)
		}
	}
}

func TestActionDiffEditFallsBackWhenFileChanged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.txt", "something else\n")
	d := ActionDiff(edit("/a.txt", "old line", "new line"), dir)
	if !strings.Contains(d, "-old line") || !strings.Contains(d, "+new line") || strings.Contains(d, "something else") {
		t.Fatalf("diff = %s", d)
	}
}

func TestActionDiffWriteNewAndExistingFile(t *testing.T) {
	dir := t.TempDir()
	created := ActionDiff(hitl.ActionRequest{Name: "write_file", Args: map[string]any{"file_path": "/new.txt", "content": "a\nb"}}, dir)
	if !strings.Contains(created, "--- /dev/null") || !strings.Contains(created, "+a") || !strings.Contains(created, "+b") {
		t.Fatalf("new file diff = %s", created)
	}
	writeFile(t, dir, "old.txt", "one\ntwo\n")
	changed := ActionDiff(hitl.ActionRequest{Name: "write_file", Args: map[string]any{"file_path": "/old.txt", "content": "one\nthree\n"}}, dir)
	if !strings.Contains(changed, "-two") || !strings.Contains(changed, "+three") || !strings.Contains(changed, " one") {
		t.Fatalf("overwrite diff = %s", changed)
	}
}

func TestActionDiffRefusesPathsOutsideCwd(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "proj")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, parent, "secret.txt", "token\n")
	d := ActionDiff(hitl.ActionRequest{Name: "write_file", Args: map[string]any{"file_path": "/../secret.txt", "content": "x\n"}}, dir)
	if strings.Contains(d, "token") {
		t.Fatalf("read outside cwd:\n%s", d)
	}
}

func TestActionDiffIsCapped(t *testing.T) {
	d := ActionDiff(hitl.ActionRequest{Name: "write_file", Args: map[string]any{"file_path": "/big.txt", "content": strings.Repeat("x\n", 500)}}, t.TempDir())
	if lines := strings.Count(d, "\n") + 1; lines > diffMaxLines+1 || !strings.Contains(d, "more diff lines") {
		t.Fatalf("lines = %d", lines)
	}
}

func TestActionDiffApplyPatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package a\n\nvar x = 1\n")
	patch := "*** Begin Patch\n*** Update File: /a.go\n@@\n-var x = 1\n+var x = 2\n*** Add File: /b.go\n+package a\n*** End Patch"
	d := ActionDiff(hitl.ActionRequest{Name: "apply_patch", Args: map[string]any{"patch": patch}}, dir)
	for _, want := range []string{"--- /a.go", "-var x = 1", "+var x = 2", " package a", "--- /dev/null", "+++ /b.go"} {
		if !strings.Contains(d, want) {
			t.Fatalf("diff missing %q:\n%s", want, d)
		}
	}
}

func TestActionPreviewInApplyPatchFallsBackToPatch(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: /missing.go\n-x\n+y\n*** End Patch"
	req := hitl.ActionRequest{Name: "apply_patch", Args: map[string]any{"patch": patch}}
	if got := ActionPreviewIn(req, t.TempDir()); !strings.Contains(got, "*** Update File: /missing.go") {
		t.Fatalf("preview = %q", got)
	}
}

func TestActionPreviewInFallsBackForExecute(t *testing.T) {
	req := hitl.ActionRequest{Name: "execute", Args: map[string]any{"command": "go test ./..."}}
	if got := ActionPreviewIn(req, t.TempDir()); got != "$ go test ./..." {
		t.Fatalf("preview = %q", got)
	}
}
