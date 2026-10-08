package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// stateCtx returns a context whose RunContext reads files and collects writes.
func stateCtx(files map[string]FileData) (context.Context, *RunContext) {
	state := &runtime.State{}
	if files != nil {
		state.Values = map[string]any{FilesStateKey: files}
	}
	rc := NewRunContext(state)
	return WithRunContext(context.Background(), rc), rc
}

func textFile(content string) FileData {
	fd, _ := CreateFileData(content, EncodingUTF8)
	return fd
}

func TestFilesReducerMergesAndDeletes(t *testing.T) {
	cur := map[string]FileData{"/a": textFile("a"), "/b": textFile("b")}
	upd := map[string]*FileData{"/b": nil, "/c": {Content: "c", Encoding: EncodingUTF8}}
	got, err := FilesReducer(cur, upd)
	if err != nil {
		t.Fatal(err)
	}
	files := got.(map[string]FileData)
	if len(files) != 2 || files["/a"].Content != "a" || files["/c"].Content != "c" {
		t.Fatalf("files = %+v", files)
	}
	if _, ok := files["/b"]; ok {
		t.Fatal("nil update must delete /b")
	}
	if len(cur) != 2 {
		t.Fatal("reducer must not modify the current map")
	}
}

func TestFilesReducerAcceptsCheckpointJSON(t *testing.T) {
	raw, _ := json.Marshal(map[string]FileData{"/a": textFile("a")})
	var generic any
	json.Unmarshal(raw, &generic)
	got, err := FilesReducer(generic, map[string]any{"/a": nil, "/b": map[string]any{"content": "b", "encoding": "utf-8"}})
	if err != nil {
		t.Fatal(err)
	}
	files := got.(map[string]FileData)
	if len(files) != 1 || files["/b"].Content != "b" {
		t.Fatalf("files = %+v", files)
	}
}

func TestFilesReducerRejectsWrongShape(t *testing.T) {
	if _, err := FilesReducer(nil, []string{"x"}); err == nil {
		t.Fatal("want error for a non-map update")
	}
}

func TestStateBackendRequiresRunContext(t *testing.T) {
	res := NewStateBackend().Ls(context.Background(), "/")
	if !strings.Contains(res.Error, "must be used inside an agent run") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestStateBackendWriteQueuesUpdateAndReadsItBack(t *testing.T) {
	b := NewStateBackend()
	ctx, rc := stateCtx(nil)
	if res := b.Write(ctx, "/src/a.go", "package a\n"); res.Error != "" {
		t.Fatal(res.Error)
	}
	upd := rc.FilesUpdate()
	if upd["/src/a.go"] == nil || upd["/src/a.go"].Content != "package a\n" {
		t.Fatalf("update = %+v", upd)
	}
	if rc.State.Values != nil {
		t.Fatal("state must not change until the update is applied")
	}
	res := b.Read(ctx, "/src/a.go", 0, 10)
	if res.Error != "" || res.FileData.Content != "package a\n" || *res.TotalLines != 1 {
		t.Fatalf("read = %+v", res)
	}
}

func TestStateBackendOverwriteKeepsCreatedAt(t *testing.T) {
	b := NewStateBackend()
	old := FileData{Content: "old", Encoding: EncodingUTF8, CreatedAt: "2020-01-01T00:00:00Z", ModifiedAt: "2020-01-01T00:00:00Z"}
	ctx, rc := stateCtx(map[string]FileData{"/a": old})
	b.Write(ctx, "/a", "new")
	got := rc.FilesUpdate()["/a"]
	if got.Content != "new" || got.CreatedAt != old.CreatedAt || got.ModifiedAt == old.ModifiedAt {
		t.Fatalf("file = %+v", got)
	}
}

func TestStateBackendReadErrors(t *testing.T) {
	b := NewStateBackend()
	ctx, _ := stateCtx(map[string]FileData{"/a.txt": textFile("one\n")})
	if res := b.Read(ctx, "/missing", 0, 10); !strings.Contains(res.Error, "not found") {
		t.Fatalf("missing: %+v", res)
	}
	if res := b.Read(ctx, "/a.txt", 5, 10); !strings.Contains(res.Error, "exceeds file length") {
		t.Fatalf("offset: %+v", res)
	}
}

func TestStateBackendEdit(t *testing.T) {
	b := NewStateBackend()
	ctx, rc := stateCtx(map[string]FileData{"/a.txt": textFile("hello world")})
	res := b.Edit(ctx, "/a.txt", "world", "go", false)
	if res.Error != "" || res.Occurrences != 1 || rc.FilesUpdate()["/a.txt"].Content != "hello go" {
		t.Fatalf("edit = %+v, update = %+v", res, rc.FilesUpdate())
	}
	if res := b.Edit(ctx, "/nope", "a", "b", false); !strings.Contains(res.Error, "not found") {
		t.Fatalf("missing: %+v", res)
	}
	if res := b.Edit(ctx, "/a.txt", "absent", "b", false); !strings.Contains(res.Error, "String not found") {
		t.Fatalf("absent: %+v", res)
	}
}

func TestStateBackendDeleteRemovesTree(t *testing.T) {
	b := NewStateBackend()
	ctx, rc := stateCtx(map[string]FileData{"/d/a": textFile("a"), "/d/sub/b": textFile("b"), "/dx": textFile("x")})
	if res := b.Delete(ctx, "/d/"); res.Error != "" {
		t.Fatal(res.Error)
	}
	upd := rc.FilesUpdate()
	if len(upd) != 2 || upd["/d/a"] != nil || upd["/d/sub/b"] != nil {
		t.Fatalf("update = %+v", upd)
	}
	if _, ok := upd["/dx"]; ok {
		t.Fatal("sibling with a shared prefix must survive")
	}
	if res := b.Ls(ctx, "/"); len(res.Entries) != 1 || res.Entries[0].Path != "/dx" {
		t.Fatalf("ls after delete = %+v", res.Entries)
	}
	if res := b.Delete(ctx, "/d"); !strings.Contains(res.Error, "not found") {
		t.Fatalf("second delete = %+v", res)
	}
}

func TestStateBackendLs(t *testing.T) {
	b := NewStateBackend()
	ctx, _ := stateCtx(map[string]FileData{"/top.txt": textFile("t"), "/sub/x.txt": textFile("x"), "/sub/deep/y.txt": textFile("y")})
	res := b.Ls(ctx, "/")
	if len(res.Entries) != 2 || res.Entries[0].Path != "/sub/" || !res.Entries[0].IsDir || res.Entries[1].Path != "/top.txt" {
		t.Fatalf("ls / = %+v", res.Entries)
	}
	res = b.Ls(ctx, "/sub")
	if len(res.Entries) != 2 || res.Entries[0].Path != "/sub/deep/" || res.Entries[1].Path != "/sub/x.txt" {
		t.Fatalf("ls /sub = %+v", res.Entries)
	}
}

func TestStateBackendGrepAndGlob(t *testing.T) {
	b := NewStateBackend()
	ctx, _ := stateCtx(map[string]FileData{"/a.go": textFile("x := 1\nneedle\n"), "/b/c.go": textFile("needle"), "/d.md": textFile("needle")})
	grep := b.Grep(ctx, "needle", GrepOptions{Glob: "*.go"})
	if grep.Error != "" || len(grep.Matches) != 2 || grep.Matches[0].Path != "/a.go" || grep.Matches[0].Line != 2 {
		t.Fatalf("grep = %+v", grep)
	}
	glob := b.Glob(ctx, "**/*.go", "/")
	if glob.Error != "" || len(glob.Matches) != 2 {
		t.Fatalf("glob = %+v", glob)
	}
	if res := b.Glob(ctx, "../*", "/"); res.Error == "" {
		t.Fatal("glob with traversal must fail")
	}
}

func TestStateBackendUploadDownload(t *testing.T) {
	b := NewStateBackend()
	ctx, rc := stateCtx(nil)
	binary := []byte{0xff, 0x00, 0x01}
	up := b.UploadFiles(ctx, []FileUpload{{Path: "/t.txt", Content: []byte("text")}, {Path: "/b.bin", Content: binary}})
	if up[0].Error != "" || up[1].Error != "" {
		t.Fatalf("upload = %+v", up)
	}
	if rc.FilesUpdate()["/b.bin"].Encoding != EncodingBase64 {
		t.Fatalf("binary upload = %+v", rc.FilesUpdate()["/b.bin"])
	}
	down := b.DownloadFiles(ctx, []string{"/t.txt", "/b.bin", "/missing"})
	if string(down[0].Content) != "text" || string(down[1].Content) != string(binary) || down[2].Error != ErrFileNotFound {
		t.Fatalf("download = %+v", down)
	}
}
