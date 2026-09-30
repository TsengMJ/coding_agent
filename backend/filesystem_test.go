package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newVirtualFS(t *testing.T) (*FilesystemBackend, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFilesystemBackend(FilesystemOptions{RootDir: dir, VirtualMode: true}), dir
}

func TestFilesystemWriteReadRoundTrip(t *testing.T) {
	b, dir := newVirtualFS(t)
	ctx := context.Background()
	if res := b.Write(ctx, "/pkg/main.go", "package main\n\nfunc main() {}\n"); res.Error != "" {
		t.Fatalf("write: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "pkg", "main.go")); err != nil {
		t.Fatalf("file not on disk: %v", err)
	}
	res := b.Read(ctx, "/pkg/main.go", 0, DefaultReadLimit)
	if res.Error != "" || res.FileData.Content != "package main\n\nfunc main() {}\n" || *res.TotalLines != 3 {
		t.Fatalf("unexpected read: %+v", res)
	}
}

func TestFilesystemReadMissingFile(t *testing.T) {
	b, _ := newVirtualFS(t)
	if res := b.Read(context.Background(), "/nope.txt", 0, 10); !strings.Contains(res.Error, "not found") {
		t.Fatalf("unexpected error: %q", res.Error)
	}
}

func TestFilesystemReadEmptyFile(t *testing.T) {
	b, dir := newVirtualFS(t)
	os.WriteFile(filepath.Join(dir, "e.txt"), nil, 0o644)
	if res := b.Read(context.Background(), "/e.txt", 0, 10); res.FileData.Content != EmptyContentWarning {
		t.Fatalf("unexpected read: %+v", res)
	}
}

func TestFilesystemEdit(t *testing.T) {
	b, dir := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/a.txt", "hello world")
	res := b.Edit(ctx, "/a.txt", "world", "go", false)
	if res.Error != "" || res.Occurrences != 1 {
		t.Fatalf("edit: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "hello go" {
		t.Fatalf("content = %q", got)
	}
}

func TestFilesystemEditAmbiguous(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/a.txt", "x x")
	if res := b.Edit(ctx, "/a.txt", "x", "y", false); !strings.Contains(res.Error, "appears 2 times") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestFilesystemLs(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/sub/x.txt", "x")
	b.Write(ctx, "/top.txt", "t")
	res := b.Ls(ctx, "/")
	if res.Error != "" || len(res.Entries) != 2 {
		t.Fatalf("unexpected ls: %+v", res)
	}
	if res.Entries[0].Path != "/sub/" || !res.Entries[0].IsDir || res.Entries[1].Path != "/top.txt" {
		t.Fatalf("unexpected entries: %+v", res.Entries)
	}
}

func TestFilesystemLsErrors(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/f.txt", "x")
	if res := b.Ls(ctx, "/missing"); !strings.Contains(res.Error, "path_not_found") {
		t.Fatalf("missing: %q", res.Error)
	}
	if res := b.Ls(ctx, "/f.txt"); !strings.Contains(res.Error, "not_a_directory") {
		t.Fatalf("file: %q", res.Error)
	}
}

func TestFilesystemDelete(t *testing.T) {
	b, dir := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/d/x.txt", "x")
	if res := b.Delete(ctx, "/d"); res.Error != "" {
		t.Fatalf("delete: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "d")); !os.IsNotExist(err) {
		t.Fatal("directory still exists")
	}
}

func TestFilesystemDeleteFile(t *testing.T) {
	b, dir := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/a.txt", "x")
	if res := b.Delete(ctx, "/a.txt"); res.Error != "" || res.Path != "/a.txt" {
		t.Fatalf("delete: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("file still exists")
	}
}

func TestFilesystemDeleteErrors(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	for path, want := range map[string]string{
		"/":        "refusing to delete the root directory",
		"/missing": "not found",
		"/../x":    "Path traversal not allowed",
	} {
		if res := b.Delete(ctx, path); !strings.Contains(res.Error, want) {
			t.Errorf("Delete(%q) = %+v, want error containing %q", path, res, want)
		}
	}
}

func TestFilesystemDeleteSymlinkRemovesOnlyLink(t *testing.T) {
	b, dir := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/target/keep.txt", "k")
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if res := b.Delete(ctx, "/link"); res.Error != "" {
		t.Fatalf("delete: %s", res.Error)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); !os.IsNotExist(err) {
		t.Fatal("link still exists")
	}
	if _, err := os.Stat(filepath.Join(dir, "target", "keep.txt")); err != nil {
		t.Fatalf("link target was deleted: %v", err)
	}
}

func TestFilesystemDeleteEscapingSymlinkRemovesOnlyLink(t *testing.T) {
	b, dir := newVirtualFS(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if res := b.Delete(context.Background(), "/link"); res.Error != "" {
		t.Fatalf("delete: %s", res.Error)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("file outside root was deleted: %v", err)
	}
}

func TestFilesystemRejectsTraversal(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	if res := b.Write(ctx, "/../escape.txt", "x"); !strings.Contains(res.Error, "Path traversal not allowed") {
		t.Fatalf("write traversal: %+v", res)
	}
	if res := b.Read(ctx, "~/x", 0, 10); !strings.Contains(res.Error, "Path traversal not allowed") {
		t.Fatalf("read home: %+v", res)
	}
}

func TestFilesystemRejectsSymlinkEscape(t *testing.T) {
	b, dir := newVirtualFS(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	ctx := context.Background()
	if res := b.Read(ctx, "/link/secret.txt", 0, 10); !strings.Contains(res.Error, "outside root") {
		t.Fatalf("read via symlink: %+v", res)
	}
	if res := b.Write(ctx, "/link/new.txt", "x"); !strings.Contains(res.Error, "outside root") {
		t.Fatalf("write via symlink: %+v", res)
	}
	if res := b.Ls(ctx, "/"); len(res.Entries) != 0 {
		t.Fatalf("escaping symlink listed: %+v", res.Entries)
	}
}

func TestFilesystemGrep(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/a.go", "package a\n// TODO fix\n")
	b.Write(ctx, "/sub/b.go", "// TODO more\n")
	b.Write(ctx, "/sub/c.md", "TODO docs\n")
	b.Write(ctx, "/.git/HEAD", "TODO git\n")

	res := b.Grep(ctx, "TODO", GrepOptions{Glob: "*.go"})
	if res.Error != "" || len(res.Matches) != 2 {
		t.Fatalf("unexpected grep: %+v", res)
	}
	if res.Matches[0].Path != "/a.go" || res.Matches[0].Line != 2 || res.Matches[1].Path != "/sub/b.go" {
		t.Fatalf("unexpected matches: %+v", res.Matches)
	}
}

func TestFilesystemGrepMaxCount(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/a.txt", "x\nx\nx\n")
	res := b.Grep(ctx, "x", GrepOptions{MaxCount: 2})
	if len(res.Matches) != 2 || !res.Truncated {
		t.Fatalf("unexpected grep: %+v", res)
	}
}

func TestFilesystemGlob(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	b.Write(ctx, "/main.go", "")
	b.Write(ctx, "/internal/x/y.go", "")
	b.Write(ctx, "/README.md", "")

	res := b.Glob(ctx, "**/*.go", "/")
	if res.Error != "" || len(res.Matches) != 2 || res.Matches[0].Path != "/internal/x/y.go" || res.Matches[1].Path != "/main.go" {
		t.Fatalf("unexpected glob: %+v", res)
	}
	sub := b.Glob(ctx, "*.go", "/internal")
	if len(sub.Matches) != 1 || sub.Matches[0].Path != "/internal/x/y.go" {
		t.Fatalf("unexpected sub glob: %+v", sub)
	}
}

func TestFilesystemUploadDownload(t *testing.T) {
	b, _ := newVirtualFS(t)
	ctx := context.Background()
	up := b.UploadFiles(ctx, []FileUpload{{Path: "/bin.dat", Content: []byte{0, 1, 2}}})
	if up[0].Error != "" {
		t.Fatalf("upload: %+v", up)
	}
	down := b.DownloadFiles(ctx, []string{"/bin.dat", "/missing"})
	if string(down[0].Content) != "\x00\x01\x02" || down[1].Error != ErrFileNotFound {
		t.Fatalf("download: %+v", down)
	}
}

func newShell(t *testing.T, opts LocalShellOptions) *LocalShellBackend {
	t.Helper()
	opts.RootDir = t.TempDir()
	opts.VirtualMode = true
	opts.InheritEnv = true
	return NewLocalShellBackend(opts)
}

func TestLocalShellExecuteRunsInRoot(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	b.Write(context.Background(), "/hello.txt", "hi")
	res := b.Execute(context.Background(), "cat hello.txt", nil)
	if res.Output != "hi" || *res.ExitCode != 0 {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestLocalShellExecuteNonZeroExit(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	res := b.Execute(context.Background(), "echo out; echo bad >&2; exit 3", nil)
	if *res.ExitCode != 3 {
		t.Fatalf("exit code = %d", *res.ExitCode)
	}
	if res.Output != "out\n\n[stderr] bad\n\nExit code: 3" {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestLocalShellExecuteTimeout(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	timeout := 100 * time.Millisecond
	res := b.Execute(context.Background(), "sleep 5", &timeout)
	if *res.ExitCode != timeoutExitCode || !strings.Contains(res.Output, "timed out") {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestLocalShellExecuteTruncates(t *testing.T) {
	b := newShell(t, LocalShellOptions{MaxOutputBytes: 10})
	res := b.Execute(context.Background(), "printf '%050d' 0", nil)
	if !res.Truncated || !strings.Contains(res.Output, "Output truncated at 10 bytes") {
		t.Fatalf("unexpected response: %+v", res)
	}
}

func TestLocalShellExecuteCapsBufferedOutput(t *testing.T) {
	b := newShell(t, LocalShellOptions{MaxOutputBytes: 1000})
	res := b.Execute(context.Background(), "head -c 5000000 /dev/zero | tr '\\0' 'x'", nil)
	if !res.Truncated || len(res.Output) > 1100 {
		t.Fatalf("truncated=%v len=%d", res.Truncated, len(res.Output))
	}
}

func TestLocalShellExecuteTruncatesOnRuneBoundary(t *testing.T) {
	b := newShell(t, LocalShellOptions{MaxOutputBytes: 10})
	res := b.Execute(context.Background(), "printf '中文中文中文中文'", nil)
	if !res.Truncated || !utf8.ValidString(res.Output) || !strings.HasPrefix(res.Output, "中文中") {
		t.Fatalf("output = %q", res.Output)
	}
}

func TestFilesystemReadRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	b := NewFilesystemBackend(FilesystemOptions{RootDir: dir, VirtualMode: true, MaxFileSizeMB: 1})
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), make([]byte, 2*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	res := b.Read(context.Background(), "/big.txt", 0, 10)
	if !strings.Contains(res.Error, "read limit") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestLocalShellExecuteEmptyCommand(t *testing.T) {
	b := newShell(t, LocalShellOptions{})
	if res := b.Execute(context.Background(), "  ", nil); *res.ExitCode != 1 {
		t.Fatalf("unexpected response: %+v", res)
	}
}
