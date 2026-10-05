package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/runtime"
)

func newTestStoreBackend() *StoreBackend {
	return NewStoreBackend(StaticNamespace(runtime.Namespace{"quill", "artifacts"}), runtime.NewInMemoryStore())
}

func TestStoreBackendWriteReadEdit(t *testing.T) {
	ctx := context.Background()
	b := newTestStoreBackend()
	if res := b.Write(ctx, "/notes/a.md", "line one\nline two\n"); res.Error != "" || res.Path != "/notes/a.md" {
		t.Fatalf("write = %+v", res)
	}
	read := b.Read(ctx, "/notes/a.md", 1, 1)
	if read.Error != "" || read.FileData == nil || read.FileData.Content != "line two\n" {
		t.Fatalf("read = %+v (%+v)", read, read.FileData)
	}
	if res := b.Edit(ctx, "/notes/a.md", "two", "2", false); res.Error != "" || res.Occurrences != 1 {
		t.Fatalf("edit = %+v", res)
	}
	if got := fullContent(t, b, "/notes/a.md"); got != "line one\nline 2\n" {
		t.Fatalf("after edit = %q", got)
	}
	if res := b.Write(ctx, "/notes/a.md", "again"); res.Error != "" {
		t.Fatalf("overwrite = %+v", res)
	}
	if got := fullContent(t, b, "/notes/a.md"); got != "again" {
		t.Fatalf("after overwrite = %q", got)
	}
}

func fullContent(t *testing.T, b Backend, p string) string {
	t.Helper()
	res := b.DownloadFiles(context.Background(), []string{p})
	if res[0].Error != "" {
		t.Fatalf("download %s: %s", p, res[0].Error)
	}
	return string(res[0].Content)
}

func TestStoreBackendLsGrepGlobDelete(t *testing.T) {
	ctx := context.Background()
	b := newTestStoreBackend()
	for p, c := range map[string]string{"/a.txt": "needle here", "/dir/b.txt": "no match", "/dir/sub/c.md": "needle again"} {
		if res := b.Write(ctx, p, c); res.Error != "" {
			t.Fatal(res.Error)
		}
	}
	ls := b.Ls(ctx, "/")
	if ls.Error != "" || len(ls.Entries) != 2 || ls.Entries[0].Path != "/a.txt" || ls.Entries[1].Path != "/dir/" || !ls.Entries[1].IsDir {
		t.Fatalf("ls = %+v", ls)
	}
	grep := b.Grep(ctx, "needle", GrepOptions{Path: "/"})
	if grep.Error != "" || len(grep.Matches) != 2 {
		t.Fatalf("grep = %+v", grep)
	}
	glob := b.Glob(ctx, "**/*.md", "/")
	if glob.Error != "" || len(glob.Matches) != 1 || glob.Matches[0].Path != "/dir/sub/c.md" {
		t.Fatalf("glob = %+v", glob)
	}
	if res := b.Delete(ctx, "/dir"); res.Error != "" {
		t.Fatalf("delete = %+v", res)
	}
	if ls := b.Ls(ctx, "/"); len(ls.Entries) != 1 {
		t.Fatalf("after delete ls = %+v", ls)
	}
	if res := b.Delete(ctx, "/missing"); res.Error == "" {
		t.Fatal("deleting a missing path must fail")
	}
}

func TestStoreBackendUploadDownloadBinary(t *testing.T) {
	ctx := context.Background()
	b := newTestStoreBackend()
	raw := []byte{0xff, 0x00, 0x10}
	up := b.UploadFiles(ctx, []FileUpload{{Path: "/bin.dat", Content: raw}, {Path: "/t.txt", Content: []byte("hi")}})
	for _, r := range up {
		if r.Error != "" {
			t.Fatalf("upload = %+v", up)
		}
	}
	down := b.DownloadFiles(ctx, []string{"/bin.dat", "/t.txt", "/none"})
	if string(down[0].Content) != string(raw) || string(down[1].Content) != "hi" || down[2].Error != ErrFileNotFound {
		t.Fatalf("download = %+v", down)
	}
}

func TestStoreBackendNotFoundHint(t *testing.T) {
	b := newTestStoreBackend().WithNotFoundHint("It may have expired; run the command again.")
	res := b.Read(context.Background(), "/gone", 0, 10)
	if !strings.Contains(res.Error, "not found") || !strings.Contains(res.Error, "may have expired") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestStoreBackendUsesRunContextStore(t *testing.T) {
	b := NewStoreBackend(nil, nil)
	if res := b.Ls(context.Background(), "/"); !strings.Contains(res.Error, "store") {
		t.Fatalf("no store must be reported, got %q", res.Error)
	}
	ctx := WithRunContext(context.Background(), &RunContext{Store: runtime.NewInMemoryStore()})
	if res := b.Write(ctx, "/x", "y"); res.Error != "" {
		t.Fatalf("write with run-context store = %+v", res)
	}
}

func TestStoreBackendAsCompositeRoute(t *testing.T) {
	ctx := context.Background()
	def := NewStateBackend()
	store := newTestStoreBackend()
	c := NewCompositeBackend(def, map[string]Backend{"/.quill/large_tool_results/": store}, "/.quill")
	if res := c.Write(ctx, "/.quill/large_tool_results/call_1", "big output"); res.Error != "" {
		t.Fatalf("write = %+v", res)
	}
	if got := fullContent(t, store, "/call_1"); got != "big output" {
		t.Fatalf("route stored %q", got)
	}
	read := c.Read(WithRunContext(ctx, NewRunContext(&runtime.State{})), "/.quill/large_tool_results/call_1", 0, 10)
	if read.Error != "" || read.FileData.Content != "big output" {
		t.Fatalf("read through composite = %+v", read)
	}
}

func TestValidateNamespace(t *testing.T) {
	if err := ValidateNamespace(runtime.Namespace{"ok", "fine"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNamespace(runtime.Namespace{"bad.part"}); err == nil {
		t.Fatal("a period must be rejected")
	}
}
