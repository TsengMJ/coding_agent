package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rt "github.com/tsengmingchun/deepagents_golang/runtime"
)

func TestProjectBackendRoutesArtifactsToStores(t *testing.T) {
	ctx := context.Background()
	cwd := t.TempDir()
	results, web, history := rt.NewInMemoryStore(), rt.NewInMemoryStore(), rt.NewInMemoryStore()
	b := NewProjectBackend(cwd, &Artifacts{Results: results, Web: web, History: history})

	for path, store := range map[string]rt.Store{
		ResultsDir + "/call-1": results,
		WebDir + "/page.md":    web,
		HistoryDir + "/t1.md":  history,
	} {
		if res := b.Write(ctx, path, "saved"); res.Error != "" {
			t.Fatalf("write %s: %s", path, res.Error)
		}
		items, err := store.Search(ctx, rt.Namespace{"dcode"}, rt.SearchOptions{})
		if err != nil || len(items) != 1 {
			t.Fatalf("%s: store items = %+v, %v", path, items, err)
		}
		if got := b.Read(ctx, path, 0, 100); got.Error != "" || got.FileData == nil || !strings.Contains(got.FileData.Content, "saved") {
			t.Fatalf("read %s = %+v", path, got)
		}
	}
	if _, err := os.Stat(filepath.Join(cwd, ".deepagents")); !os.IsNotExist(err) {
		t.Fatalf("artifacts written to the project tree: %v", err)
	}
	if res := b.Write(ctx, "/main.go", "package main\n"); res.Error != "" {
		t.Fatal(res.Error)
	}
	if _, err := os.Stat(filepath.Join(cwd, "main.go")); err != nil {
		t.Fatalf("project file not on disk: %v", err)
	}
}

func TestExpiredResultExplainsItself(t *testing.T) {
	b := NewProjectBackend(t.TempDir(), &Artifacts{Results: rt.NewInMemoryStore()})
	got := b.Read(context.Background(), ResultsDir+"/gone", 0, 100)
	if !strings.Contains(got.Error, "expired") {
		t.Fatalf("error = %q", got.Error)
	}
}

func TestProjectsDoNotShareArtifacts(t *testing.T) {
	ctx := context.Background()
	results := rt.NewInMemoryStore()
	a := &Artifacts{Results: results}
	if res := NewProjectBackend(t.TempDir(), a).Write(ctx, ResultsDir+"/x", "one"); res.Error != "" {
		t.Fatal(res.Error)
	}
	if got := NewProjectBackend(t.TempDir(), a).Read(ctx, ResultsDir+"/x", 0, 100); got.Error == "" {
		t.Fatalf("other project read %+v", got.FileData)
	}
}

func TestNilArtifactsKeepFilesOnBackend(t *testing.T) {
	cwd := t.TempDir()
	if res := NewProjectBackend(cwd, nil).Write(context.Background(), ResultsDir+"/x", "one"); res.Error != "" {
		t.Fatal(res.Error)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".deepagents", "large_tool_results", "x")); err != nil {
		t.Fatal(err)
	}
}
