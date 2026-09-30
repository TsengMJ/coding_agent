package backend

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fsAt(t *testing.T, files map[string]string) *FilesystemBackend {
	t.Helper()
	dir := t.TempDir()
	for p, content := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewFilesystemBackend(FilesystemOptions{RootDir: dir, VirtualMode: true})
}

func paths[T any](items []T, get func(T) string) []string {
	var out []string
	for _, it := range items {
		out = append(out, get(it))
	}
	return out
}

func infoPath(fi FileInfo) string          { return fi.Path }
func matchPath(m GrepMatch) string         { return m.Path }
func dlPath(d FileDownloadResponse) string { return d.Path }

func TestCompositeRoutesLongestPrefixAndRewritesPaths(t *testing.T) {
	ctx := context.Background()
	def := fsAt(t, map[string]string{"a.txt": "default"})
	mem := fsAt(t, map[string]string{"note.md": "memories"})
	deep := fsAt(t, map[string]string{"x.md": "deep"})
	c := NewCompositeBackend(def, map[string]Backend{"/memories": mem, "/memories/deep/": deep}, "")

	if r := c.Read(ctx, "/memories/note.md", 0, 10); r.Error != "" || r.FileData.Content != "memories" {
		t.Fatalf("routed read = %+v", r)
	}
	if r := c.Read(ctx, "/memories/deep/x.md", 0, 10); r.Error != "" || r.FileData.Content != "deep" {
		t.Fatalf("longest prefix read = %+v", r)
	}
	if r := c.Read(ctx, "/a.txt", 0, 10); r.Error != "" || r.FileData.Content != "default" {
		t.Fatalf("default read = %+v", r)
	}
	if w := c.Write(ctx, "/memories/new.md", "n"); w.Error != "" || w.Path != "/memories/new.md" {
		t.Fatalf("write = %+v", w)
	}
	if r := mem.Read(ctx, "/new.md", 0, 10); r.Error != "" {
		t.Fatalf("write did not land in the route backend: %+v", r)
	}
	if e := c.Edit(ctx, "/memories/new.md", "n", "m", false); e.Error != "" || e.Path != "/memories/new.md" {
		t.Fatalf("edit = %+v", e)
	}
	ls := c.Ls(ctx, "/memories")
	if ls.Error != "" || !slices.Contains(paths(ls.Entries, infoPath), "/memories/note.md") {
		t.Fatalf("routed ls = %+v", ls)
	}
}

func TestCompositeRootLsListsRoutes(t *testing.T) {
	c := NewCompositeBackend(fsAt(t, map[string]string{"a.txt": "x"}), map[string]Backend{"/memories/": fsAt(t, nil)}, "")
	ls := c.Ls(context.Background(), "/")
	got := paths(ls.Entries, infoPath)
	if ls.Error != "" || !slices.Contains(got, "/memories/") || !slices.Contains(got, "/a.txt") {
		t.Fatalf("root ls = %v (%s)", got, ls.Error)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("root ls not sorted: %v", got)
	}
}

func TestCompositeGrepMergesAndSharesBudget(t *testing.T) {
	ctx := context.Background()
	def := fsAt(t, map[string]string{"a.txt": "needle\nneedle\n"})
	mem := fsAt(t, map[string]string{"b.txt": "needle\n"})
	c := NewCompositeBackend(def, map[string]Backend{"/memories/": mem}, "")

	all := c.Grep(ctx, "needle", GrepOptions{})
	if all.Error != "" || !slices.Equal(paths(all.Matches, matchPath), []string{"/a.txt", "/a.txt", "/memories/b.txt"}) || all.Truncated {
		t.Fatalf("merged grep = %+v", all)
	}
	capped := c.Grep(ctx, "needle", GrepOptions{Path: "/", MaxCount: 2})
	if len(capped.Matches) != 2 || !capped.Truncated {
		t.Fatalf("budget grep = %+v", capped)
	}
	routed := c.Grep(ctx, "needle", GrepOptions{Path: "/memories"})
	if !slices.Equal(paths(routed.Matches, matchPath), []string{"/memories/b.txt"}) {
		t.Fatalf("routed grep = %+v", routed)
	}
}

func TestCompositeGlobRewritesPatterns(t *testing.T) {
	ctx := context.Background()
	def := fsAt(t, map[string]string{"top.md": "", "sub/inner.md": ""})
	mem := fsAt(t, map[string]string{"n.md": "", "d/deep.md": ""})
	c := NewCompositeBackend(def, map[string]Backend{"/memories/": mem}, "")

	bare := paths(c.Glob(ctx, "**/*.md", "/").Matches, infoPath)
	for _, want := range []string{"/memories/d/deep.md", "/memories/n.md", "/sub/inner.md"} {
		if !slices.Contains(bare, want) {
			t.Errorf("bare glob missing %s: %v", want, bare)
		}
	}
	if !slices.IsSorted(bare) {
		t.Errorf("glob not sorted: %v", bare)
	}
	targeted := paths(c.Glob(ctx, "/memories/*.md", "").Matches, infoPath)
	if !slices.Contains(targeted, "/memories/n.md") || slices.Contains(targeted, "/memories/d/deep.md") {
		t.Errorf("route-targeted glob = %v", targeted)
	}
	anchored := paths(c.Glob(ctx, "/*.md", "/").Matches, infoPath)
	if slices.ContainsFunc(anchored, func(p string) bool { return strings.HasPrefix(p, "/memories/") }) {
		t.Errorf("anchored glob leaked into routes: %v", anchored)
	}
}

func TestMergeTruncationReasonPrefersUnreadable(t *testing.T) {
	if got := mergeTruncationReason(GlobTruncatedBudget, GlobTruncatedUnreadable); got != GlobTruncatedUnreadable {
		t.Fatalf("got %q", got)
	}
	if got := mergeTruncationReason("", GlobTruncatedBudget); got != GlobTruncatedBudget {
		t.Fatalf("got %q", got)
	}
}

func TestCompositeUploadDownloadKeepOrder(t *testing.T) {
	ctx := context.Background()
	def, mem := fsAt(t, nil), fsAt(t, nil)
	c := NewCompositeBackend(def, map[string]Backend{"/memories/": mem}, "")
	up := c.UploadFiles(ctx, []FileUpload{
		{Path: "/memories/1", Content: []byte("one")},
		{Path: "/2", Content: []byte("two")},
		{Path: "/memories/3", Content: []byte("three")},
	})
	for i, want := range []string{"/memories/1", "/2", "/memories/3"} {
		if up[i].Path != want || up[i].Error != "" {
			t.Fatalf("upload[%d] = %+v", i, up[i])
		}
	}
	down := c.DownloadFiles(ctx, []string{"/2", "/memories/3", "/memories/1"})
	if got := paths(down, dlPath); !slices.Equal(got, []string{"/2", "/memories/3", "/memories/1"}) {
		t.Fatalf("download order = %v", got)
	}
	if string(down[1].Content) != "three" {
		t.Fatalf("download content = %q", down[1].Content)
	}
}

func TestCompositeCapabilitiesFollowDefault(t *testing.T) {
	state := NewCompositeBackend(NewStateBackend(), nil, "")
	if SupportsExecution(state) {
		t.Fatal("non-sandbox default must not offer execute")
	}
	if !SupportsDelete(state) {
		t.Fatal("deleting default must offer delete")
	}
	shell := NewCompositeBackend(NewLocalShellBackend(LocalShellOptions{}), map[string]Backend{"/r/": fsAt(t, nil)}, "/.deepagents")
	if !SupportsExecution(shell) {
		t.Fatal("sandbox default must offer execute")
	}
	if got := ArtifactsRoot(shell); got != "/.deepagents" {
		t.Fatalf("artifacts root = %q", got)
	}
	if got := ArtifactsRoot(NewStateBackend()); got != "/" {
		t.Fatalf("default artifacts root = %q", got)
	}
	type readOnly struct{ Backend }
	if SupportsDelete(NewCompositeBackend(readOnly{fsAt(t, nil)}, nil, "")) {
		t.Fatal("non-deleting default must not offer delete")
	}
}

func TestCompositeDeleteUnsupportedRoute(t *testing.T) {
	type readOnly struct{ Backend }
	c := NewCompositeBackend(NewLocalShellBackend(LocalShellOptions{FilesystemOptions: FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true}}), map[string]Backend{"/ro/": readOnly{fsAt(t, map[string]string{"f": "x"})}}, "")
	d, ok := c.(Deleter)
	if !ok {
		t.Fatal("composite over a deleting default must implement Deleter")
	}
	if r := d.Delete(context.Background(), "/ro/f"); !strings.Contains(r.Error, "deletion is not supported for '/ro/f'") {
		t.Fatalf("delete = %+v", r)
	}
}
