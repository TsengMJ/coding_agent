package codetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func newFS(t *testing.T) (string, *backend.FilesystemBackend) {
	t.Helper()
	dir := t.TempDir()
	return dir, backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true})
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func patchArgs(p string) map[string]any { return map[string]any{"patch": p} }

func TestApplyPatchFilesystem(t *testing.T) {
	dir, fs := newFS(t)
	writeFile(t, dir, "src/app.go", "package src\n\nfunc main() {\n\tcfg := load()\n\trun(cfg)\n}\n")
	writeFile(t, dir, "src/old.go", "package src\n")
	writeFile(t, dir, "src/a.go", "package src\n\nvar x = 1\n")

	patch := `*** Begin Patch
*** Update File: /src/app.go
@@ func main() {
 	cfg := load()
-	run(cfg)
+	if err := run(cfg); err != nil {
+		panic(err)
+	}
*** Add File: src/new.go
+package src
*** Delete File: /src/old.go
*** Update File: /src/a.go
*** Move to: /src/b.go
@@
-var x = 1
+var x = 2
*** End Patch`
	got := decode[PatchResult](t, invoke(t, NewApplyPatch(fs), patchArgs(patch)))
	want := []PatchFileResult{
		{Path: "/src/app.go", Op: OpUpdate, Additions: 3, Deletions: 1},
		{Path: "/src/new.go", Op: OpAdd, Additions: 1},
		{Path: "/src/old.go", Op: OpDelete, Deletions: 1},
		{Path: "/src/b.go", MovedFrom: "/src/a.go", Op: OpUpdate, Additions: 1, Deletions: 1},
	}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v", got.Files)
	}
	for i := range want {
		if got.Files[i] != want[i] {
			t.Errorf("file %d = %+v, want %+v", i, got.Files[i], want[i])
		}
	}
	if s := readFile(t, dir, "src/app.go"); !strings.Contains(s, "if err := run(cfg); err != nil {\n\t\tpanic(err)\n\t}\n}") {
		t.Errorf("app.go = %q", s)
	}
	if s := readFile(t, dir, "src/new.go"); s != "package src\n" {
		t.Errorf("new.go = %q", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/old.go")); !os.IsNotExist(err) {
		t.Error("old.go still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, "src/a.go")); !os.IsNotExist(err) {
		t.Error("a.go was not moved")
	}
	if s := readFile(t, dir, "src/b.go"); s != "package src\n\nvar x = 2\n" {
		t.Errorf("b.go = %q", s)
	}
}

func TestApplyPatchFailureChangesNothing(t *testing.T) {
	dir, fs := newFS(t)
	writeFile(t, dir, "a.go", "one\n")
	writeFile(t, dir, "b.go", "two\n")
	patch := "*** Begin Patch\n*** Update File: /a.go\n-one\n+ONE\n*** Update File: /b.go\n-missing\n+x\n*** Add File: /c.go\n+c\n*** End Patch"
	res := invoke(t, NewApplyPatch(fs), patchArgs(patch))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "no files were changed") || !strings.Contains(res.Content[0].Text, "missing") {
		t.Fatalf("got %q", res.Content[0].Text)
	}
	if readFile(t, dir, "a.go") != "one\n" {
		t.Error("a.go changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.go")); !os.IsNotExist(err) {
		t.Error("c.go created")
	}
}

func TestApplyPatchPreconditions(t *testing.T) {
	dir, fs := newFS(t)
	writeFile(t, dir, "exists.go", "x\n")
	writeFile(t, dir, "dest.go", "y\n")
	tests := []struct{ name, patch, want string }{
		{"add existing", "*** Begin Patch\n*** Add File: /exists.go\n+x\n*** End Patch", "already exists"},
		{"update missing", "*** Begin Patch\n*** Update File: /nope.go\n-x\n+y\n*** End Patch", "file not found"},
		{"delete missing", "*** Begin Patch\n*** Delete File: /nope.go\n*** End Patch", "file not found"},
		{"move onto existing", "*** Begin Patch\n*** Update File: /exists.go\n*** Move to: /dest.go\n-x\n+z\n*** End Patch", "destination already exists"},
		{"escape", "*** Begin Patch\n*** Delete File: ../etc/passwd\n*** End Patch", "traversal"},
		{"parse error", "*** Begin Patch\n*** End Patch", "no file operations"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := invoke(t, NewApplyPatch(fs), patchArgs(tt.patch))
			if !res.IsError || !strings.Contains(res.Content[0].Text, tt.want) {
				t.Fatalf("got %q, want %q", res.Content[0].Text, tt.want)
			}
		})
	}
}

func TestApplyPatchStateBackend(t *testing.T) {
	fd, err := backend.CreateFileData("hello world\n", backend.EncodingUTF8)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtime.State{Values: map[string]any{backend.FilesStateKey: map[string]*backend.FileData{"/notes/a.md": &fd}}}
	patch := "*** Begin Patch\n*** Update File: /notes/a.md\n-hello world\n+hello state\n*** Add File: /notes/b.md\n+new\n*** End Patch"
	res, err := NewApplyPatch(backend.NewStateBackend()).Invoke(context.Background(), &tool.Runtime{State: state}, patchArgs(patch))
	if err != nil || res.IsError {
		t.Fatalf("err=%v res=%+v", err, res.Content)
	}
	if res.Update == nil {
		t.Fatal("no files update")
	}
	files := res.Update.Values[backend.FilesStateKey].(map[string]*backend.FileData)
	if files["/notes/a.md"].Content != "hello state\n" || files["/notes/b.md"].Content != "new\n" {
		t.Fatalf("files = a:%q b:%q", files["/notes/a.md"].Content, files["/notes/b.md"].Content)
	}
}

func TestApplyPatchRejectsParallelMutation(t *testing.T) {
	_, fs := newFS(t)
	state := &runtime.State{Messages: []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "w", Name: "write_file", Args: map[string]any{"file_path": "/a.go", "content": "x"}},
		{ID: "p", Name: ApplyPatchName, Args: map[string]any{"patch": "..."}},
	}}}}
	patch := "*** Begin Patch\n*** Add File: /a.go\n+x\n*** End Patch"
	res, _ := NewApplyPatch(fs).Invoke(context.Background(), &tool.Runtime{State: state, ToolCallID: "p"}, patchArgs(patch))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "parallel file mutations") {
		t.Fatalf("got %q", res.Content[0].Text)
	}

	state.Messages[0].ToolCalls[0] = llm.ToolCall{ID: "q", Name: ApplyPatchName, Args: map[string]any{"patch": "*** Begin Patch\n*** Delete File: /a.go\n*** End Patch"}}
	res, _ = NewApplyPatch(fs).Invoke(context.Background(), &tool.Runtime{State: state, ToolCallID: "p"}, patchArgs(patch))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "parallel file mutations") {
		t.Fatalf("earlier apply_patch: got %q", res.Content[0].Text)
	}
}

// failingWriter fails writes to one path to exercise rollback.
type failingWriter struct {
	*backend.FilesystemBackend
	fail string
}

func (f failingWriter) Write(ctx context.Context, p, content string) backend.WriteResult {
	if p == f.fail {
		return backend.WriteResult{Error: "disk full"}
	}
	return f.FilesystemBackend.Write(ctx, p, content)
}

func TestApplyPatchRollsBackOnWriteFailure(t *testing.T) {
	dir, fs := newFS(t)
	writeFile(t, dir, "a.go", "one\n")
	writeFile(t, dir, "b.go", "two\n")
	patch := "*** Begin Patch\n*** Update File: /a.go\n-one\n+ONE\n*** Add File: /n.go\n+n\n*** Update File: /b.go\n-two\n+TWO\n*** End Patch"
	res := invoke(t, NewApplyPatch(failingWriter{fs, "/b.go"}), patchArgs(patch))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "rolled back") {
		t.Fatalf("got %q", res.Content[0].Text)
	}
	if readFile(t, dir, "a.go") != "one\n" {
		t.Error("a.go not restored")
	}
	if _, err := os.Stat(filepath.Join(dir, "n.go")); !os.IsNotExist(err) {
		t.Error("n.go not removed")
	}
}

// meddlingWriter changes another file just before its failing write, as
// an editor saving while the patch runs would.
type meddlingWriter struct {
	failingWriter
	dir string
}

func (m meddlingWriter) Write(ctx context.Context, p, content string) backend.WriteResult {
	if p == m.fail {
		_ = os.WriteFile(filepath.Join(m.dir, "a.go"), []byte("ONE\nsaved by the user\n"), 0o644)
	}
	return m.failingWriter.Write(ctx, p, content)
}

func TestApplyPatchRollbackKeepsFilesChangedMeanwhile(t *testing.T) {
	dir, fs := newFS(t)
	writeFile(t, dir, "a.go", "one\n")
	writeFile(t, dir, "b.go", "two\n")
	patch := "*** Begin Patch\n*** Update File: /a.go\n-one\n+ONE\n*** Add File: /n.go\n+n\n*** Update File: /b.go\n-two\n+TWO\n*** End Patch"
	res := invoke(t, NewApplyPatch(meddlingWriter{failingWriter{fs, "/b.go"}, dir}), patchArgs(patch))
	if !res.IsError || !strings.Contains(res.Content[0].Text, "could not be fully rolled back: /a.go") {
		t.Fatalf("got %q", res.Content[0].Text)
	}
	if got := readFile(t, dir, "a.go"); got != "ONE\nsaved by the user\n" {
		t.Errorf("rollback overwrote the user's change: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "n.go")); !os.IsNotExist(err) {
		t.Error("n.go was unchanged and should still be removed")
	}
}

func TestPatchedFileUnifiedDiff(t *testing.T) {
	f := PatchedFile{Op: OpUpdate, Path: "/a.go", Before: "a\nb\n", After: "a\nc\n"}
	if d := f.UnifiedDiff(); !strings.Contains(d, "-b") || !strings.Contains(d, "+c") {
		t.Fatalf("diff = %q", d)
	}
}
