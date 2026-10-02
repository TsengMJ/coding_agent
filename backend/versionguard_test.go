package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func guardedShell(t *testing.T) (Backend, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := WithVersionGuard(NewLocalShellBackend(LocalShellOptions{FilesystemOptions: FilesystemOptions{RootDir: dir, VirtualMode: true}}))
	return b, dir
}

func TestVersionGuardRequiresReadBeforeChange(t *testing.T) {
	b, _ := guardedShell(t)
	ctx := context.Background()
	if res := b.Edit(ctx, "/a.go", "package a", "package b", false); !strings.Contains(res.Error, "has not been read") {
		t.Fatalf("edit before read: %+v", res)
	}
	if res := b.Write(ctx, "/a.go", "x"); !strings.Contains(res.Error, "has not been read") {
		t.Fatalf("overwrite before read: %+v", res)
	}
	if res := b.Write(ctx, "/new.go", "package n\n"); res.Error != "" {
		t.Fatalf("creating a file needs no read: %+v", res)
	}
	if res := b.Edit(ctx, "/new.go", "package n", "package m", false); res.Error != "" {
		t.Fatalf("a file the agent wrote counts as read: %+v", res)
	}
	b.Read(ctx, "/a.go", 0, 1)
	if res := b.Edit(ctx, "/a.go", "package a", "package b", false); res.Error != "" {
		t.Fatalf("edit after read: %+v", res)
	}
	if res := b.Edit(ctx, "/a.go", "package b", "package c", false); res.Error != "" {
		t.Fatalf("a second edit needs no new read: %+v", res)
	}
}

func TestVersionGuardRejectsStaleVersion(t *testing.T) {
	b, dir := guardedShell(t)
	ctx := context.Background()
	b.Read(ctx, "/a.go", 0, 100)
	// Someone else changes the file after the agent read it.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc User() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res := b.Write(ctx, "/a.go", "package a\n"); !strings.Contains(res.Error, "changed since you last read it") {
		t.Fatalf("stale overwrite: %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if !strings.Contains(string(got), "func User") {
		t.Fatalf("the other change was lost: %q", got)
	}
	// A command the agent ran counts as a change too.
	b.Read(ctx, "/a.go", 0, 100)
	b.(SandboxBackend).Execute(ctx, "echo '// generated' >> a.go", nil)
	if res := b.Edit(ctx, "/a.go", "package a", "package b", false); !strings.Contains(res.Error, "changed since") {
		t.Fatalf("edit after a command changed the file: %+v", res)
	}
	b.Read(ctx, "/a.go", 0, 100)
	if res := b.Edit(ctx, "/a.go", "package a", "package b", false); res.Error != "" {
		t.Fatalf("edit after re-read: %+v", res)
	}
}

func TestVersionGuardKeepsCapabilitiesAndIsIdempotent(t *testing.T) {
	b, _ := guardedShell(t)
	if !SupportsExecution(b) || !SupportsDelete(b) {
		t.Fatal("guard dropped execution or delete")
	}
	if WithVersionGuard(b) != b {
		t.Fatal("guarding twice must return the same backend")
	}
	if SupportsExecution(WithVersionGuard(NewFilesystemBackend(FilesystemOptions{RootDir: t.TempDir()}))) {
		t.Fatal("guard added execution")
	}
}
