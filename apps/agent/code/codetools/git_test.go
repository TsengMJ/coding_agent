package codetools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

func newShell(t *testing.T, dir string) *backend.LocalShellBackend {
	t.Helper()
	return backend.NewLocalShellBackend(backend.LocalShellOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: dir, VirtualMode: true},
		InheritEnv:        true,
	})
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	base := []string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}
	if out, err := exec.Command("git", append(base, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	return dir
}

func invoke(t *testing.T, tl tool.Tool, args map[string]any) *tool.Result {
	t.Helper()
	res, err := tl.Invoke(context.Background(), &tool.Runtime{}, args)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decode[T any](t *testing.T, res *tool.Result) T {
	t.Helper()
	if res.IsError {
		t.Fatalf("tool error: %s", res.Content[0].Text)
	}
	var v T
	if err := json.Unmarshal([]byte(res.Content[0].Text), &v); err != nil {
		t.Fatalf("decode %q: %v", res.Content[0].Text, err)
	}
	return v
}

func TestGitStatusEndToEnd(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "keep.go", "package a\n")
	writeFile(t, dir, "gone.go", "package a\n")
	writeFile(t, dir, "old.go", "package a\n\nfunc Old() {}\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "init")

	writeFile(t, dir, "keep.go", "package a\n\nvar X = 1\n")
	writeFile(t, dir, "new file.go", "package a\n")
	gitCmd(t, dir, "add", "new file.go")
	os.Remove(filepath.Join(dir, "gone.go"))
	gitCmd(t, dir, "mv", "old.go", "renamed.go")
	writeFile(t, dir, "scratch.txt", "x\n")

	st := decode[GitStatus](t, invoke(t, NewGitStatus(newShell(t, dir)), nil))
	if st.Branch != "main" {
		t.Errorf("branch = %q", st.Branch)
	}
	check := func(name string, got, want []string) {
		if !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	check("modified", st.Modified, []string{"keep.go"})
	check("added", st.Added, []string{"new file.go"})
	check("deleted", st.Deleted, []string{"gone.go"})
	check("untracked", st.Untracked, []string{"scratch.txt"})
	if len(st.Renamed) != 1 || st.Renamed[0] != (Renamed{From: "old.go", To: "renamed.go"}) {
		t.Errorf("renamed = %v", st.Renamed)
	}
}

func TestGitStatusSubdirectoryIsRelative(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "sub/b.go", "package a\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "init")
	writeFile(t, dir, "sub/a.go", "package a\n")
	writeFile(t, dir, "other.go", "package a\n")
	st := decode[GitStatus](t, invoke(t, NewGitStatus(newShell(t, filepath.Join(dir, "sub"))), nil))
	if !slices.Equal(st.Untracked, []string{"a.go"}) {
		t.Fatalf("untracked = %v", st.Untracked)
	}

	fresh := newRepo(t)
	writeFile(t, fresh, "sub/a.go", "package a\n")
	st = decode[GitStatus](t, invoke(t, NewGitStatus(newShell(t, filepath.Join(fresh, "sub"))), nil))
	if !slices.Equal(st.Untracked, []string{"./"}) {
		t.Fatalf("whole untracked dir = %v", st.Untracked)
	}
}

func TestGitStatusOutsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	res := invoke(t, NewGitStatus(newShell(t, dir)), nil)
	if !res.IsError || !strings.Contains(res.Content[0].Text, "not a git repository") {
		t.Fatalf("got %+v", res.Content)
	}
}

func TestGitDiffEndToEnd(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "internal/ws/client.go", "package ws\n\nvar a = 1\nvar b = 2\n")
	writeFile(t, dir, "logo.png", "\x89PNG\x00\x01")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "init")

	writeFile(t, dir, "internal/ws/client.go", "package ws\n\nvar a = 10\nvar b = 2\nvar c = 3\n")
	writeFile(t, dir, "logo.png", "\x89PNG\x00\x02")
	writeFile(t, dir, "notes.md", "one\ntwo\n")
	sb := newShell(t, dir)

	got := decode[GitDiffResult](t, invoke(t, NewGitDiff(sb), nil))
	byPath := map[string]FileDiff{}
	for _, f := range got.Files {
		byPath[f.Path] = f
	}
	c := byPath["internal/ws/client.go"]
	if c.Status != "modified" || c.Additions != 2 || c.Deletions != 1 || !strings.Contains(c.Diff, "+var c = 3") {
		t.Errorf("client.go = %+v", c)
	}
	if p := byPath["logo.png"]; !p.Binary || p.Diff != "" {
		t.Errorf("logo.png = %+v", p)
	}
	if n := byPath["notes.md"]; n.Status != "untracked" || n.Additions != 2 || !strings.Contains(n.Diff, "+two") {
		t.Errorf("notes.md = %+v", n)
	}

	only := decode[GitDiffResult](t, invoke(t, NewGitDiff(sb), map[string]any{"paths": []any{"/internal"}}))
	if len(only.Files) != 1 || only.Files[0].Path != "internal/ws/client.go" {
		t.Errorf("paths filter = %+v", only.Files)
	}

	staged := decode[GitDiffResult](t, invoke(t, NewGitDiff(sb), map[string]any{"staged": true}))
	if len(staged.Files) != 0 {
		t.Errorf("staged = %+v", staged.Files)
	}
}

func TestGitDiffBeforeFirstCommit(t *testing.T) {
	dir := newRepo(t)
	writeFile(t, dir, "a.go", "package a\n")
	gitCmd(t, dir, "add", "a.go")
	got := decode[GitDiffResult](t, invoke(t, NewGitDiff(newShell(t, dir)), map[string]any{"staged": true}))
	if len(got.Files) != 1 || got.Files[0].Status != "added" || got.Files[0].Additions != 1 {
		t.Fatalf("got %+v", got.Files)
	}
}

func TestGitDiffRejectsEscapingPath(t *testing.T) {
	res := invoke(t, NewGitDiff(newShell(t, t.TempDir())), map[string]any{"paths": []any{"../x"}})
	if !res.IsError {
		t.Fatal("expected error")
	}
}

func TestBudgetDiffs(t *testing.T) {
	big := strings.Repeat("+line\n", maxFileDiffBytes/3)
	files := make([]FileDiff, 6)
	for i := range files {
		files[i] = FileDiff{Path: "f", Diff: big}
	}
	if !budgetDiffs(files) {
		t.Fatal("expected cut")
	}
	if len(files[0].Diff) > maxFileDiffBytes+64 || !strings.Contains(files[0].Diff, "more bytes clipped") {
		t.Errorf("first diff not clipped: %d bytes", len(files[0].Diff))
	}
	if !files[5].DiffOmitted || files[5].Diff != "" {
		t.Errorf("last diff should be omitted: %+v", files[5].DiffOmitted)
	}
}

func TestNewWithoutExecution(t *testing.T) {
	for _, tl := range New(backend.NewStateBackend()) {
		if tl.Name() == GitStatusName || tl.Name() == GitDiffName {
			t.Fatalf("%s offered without execution", tl.Name())
		}
	}
	var names []string
	for _, tl := range New(newShell(t, t.TempDir())) {
		names = append(names, tl.Name())
	}
	if !slices.Contains(names, GitStatusName) || !slices.Contains(names, GitDiffName) {
		t.Fatalf("names = %v", names)
	}
}
