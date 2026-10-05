package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/permissions"
)

// newGuarded returns a middleware over a temp-dir backend seeded with
// /pub/a.txt, /secrets/key.txt and /data/keep/x.txt, denying read+write on
// /secrets and write on /data/keep.
func newGuarded(t *testing.T) (*Middleware, string) {
	t.Helper()
	dir := t.TempDir()
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true})
	for p, content := range map[string]string{
		"/pub/a.txt":       "hello\n",
		"/secrets/key.txt": "hello secret\n",
		"/data/keep/x.txt": "keep\n",
		"/data/tmp.txt":    "tmp\n",
	} {
		if res := b.Write(context.Background(), p, content); res.Error != "" {
			t.Fatal(res.Error)
		}
	}
	both := []permissions.Operation{permissions.OpRead, permissions.OpWrite}
	m, err := New(Options{Backend: b, Permissions: []permissions.FilesystemPermission{
		{Operations: both, Paths: []string{"/secrets", "/secrets/**"}, Mode: permissions.ModeDeny},
		{Operations: []permissions.Operation{permissions.OpWrite}, Paths: []string{"/data/keep/**"}, Mode: permissions.ModeDeny},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

func TestPermissionsDenyReadAndWrite(t *testing.T) {
	m, dir := newGuarded(t)
	msgs, _ := runAgent(t, m,
		call("r", "read_file", map[string]any{"file_path": "/secrets/key.txt"}),
		call("w", "write_file", map[string]any{"file_path": "/secrets/new.txt", "content": "x"}),
		call("e", "edit_file", map[string]any{"file_path": "/data/keep/x.txt", "old_string": "keep", "new_string": "gone"}),
		call("ls", "ls", map[string]any{"path": "/secrets"}),
		call("ok", "read_file", map[string]any{"file_path": "/pub/a.txt"}),
	)
	for id, want := range map[string]string{
		"r":  "Error: permission denied for read on /secrets/key.txt",
		"w":  "Error: permission denied for write on /secrets/new.txt",
		"e":  "Error: permission denied for write on /data/keep/x.txt",
		"ls": "Error: permission denied for read on /secrets",
	} {
		if msgs[id].Status != llm.ToolStatusError || msgs[id].Text() != want {
			t.Errorf("%s: status=%s text=%q, want %q", id, msgs[id].Status, msgs[id].Text(), want)
		}
	}
	if msgs["ok"].Status != llm.ToolStatusSuccess {
		t.Errorf("allowed read failed: %q", msgs["ok"].Text())
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "new.txt")); !os.IsNotExist(err) {
		t.Fatal("denied write reached the disk")
	}
}

func TestPermissionsFilterListings(t *testing.T) {
	m, _ := newGuarded(t)
	msgs, _ := runAgent(t, m,
		call("ls", "ls", map[string]any{"path": "/"}),
		call("glob", "glob", map[string]any{"pattern": "**/*.txt", "path": "/"}),
		call("grep", "grep", map[string]any{"pattern": "hello"}),
	)
	for id, text := range map[string]string{"ls": msgs["ls"].Text(), "glob": msgs["glob"].Text(), "grep": msgs["grep"].Text()} {
		if strings.Contains(text, "/secrets") {
			t.Errorf("%s leaked a denied path: %q", id, text)
		}
	}
	if !strings.Contains(msgs["grep"].Text(), "/pub/a.txt") || !strings.Contains(msgs["glob"].Text(), "/pub/a.txt") {
		t.Errorf("allowed matches missing: grep=%q glob=%q", msgs["grep"].Text(), msgs["glob"].Text())
	}
}

func TestPermissionsPathlessGlobDeniedHint(t *testing.T) {
	dir := t.TempDir()
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true})
	m, err := New(Options{Backend: b, Permissions: []permissions.FilesystemPermission{
		{Operations: []permissions.Operation{permissions.OpRead}, Paths: []string{"/"}, Mode: permissions.ModeDeny},
	}})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := runAgent(t, m, call("g", "glob", map[string]any{"pattern": "/pub/*.txt"}))
	if !strings.Contains(msgs["g"].Text(), "Retry with an explicit 'path'") {
		t.Fatalf("glob hint missing: %q", msgs["g"].Text())
	}
}

func TestPermissionsDeleteChecksDescendants(t *testing.T) {
	m, dir := newGuarded(t)
	msgs, _ := runAgent(t, m,
		call("dir", "delete", map[string]any{"file_path": "/data"}),
		call("file", "delete", map[string]any{"file_path": "/data/tmp.txt"}),
	)
	if want := "Error: permission denied for write on /data (matches deny rule(s): /data/keep/**)"; msgs["dir"].Text() != want {
		t.Errorf("dir delete = %q, want %q", msgs["dir"].Text(), want)
	}
	if msgs["file"].Status != llm.ToolStatusSuccess {
		t.Errorf("file delete failed: %q", msgs["file"].Text())
	}
	if _, err := os.Stat(filepath.Join(dir, "data", "keep", "x.txt")); err != nil {
		t.Fatal("protected file was removed")
	}
}

func TestPermissionsRejectedWithExecutionBackend(t *testing.T) {
	b, _ := newShellBackend(t)
	_, err := New(Options{Backend: b, Permissions: []permissions.FilesystemPermission{
		{Operations: []permissions.Operation{permissions.OpRead}, Paths: []string{"/x"}, Mode: permissions.ModeDeny},
	}})
	if err == nil || !strings.Contains(err.Error(), "command execution") {
		t.Fatalf("err = %v", err)
	}
}

func TestPermissionsInvalidRuleRejected(t *testing.T) {
	b := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: t.TempDir(), VirtualMode: true})
	_, err := New(Options{Backend: b, Permissions: []permissions.FilesystemPermission{
		{Operations: []permissions.Operation{permissions.OpRead}, Paths: []string{"relative"}, Mode: permissions.ModeDeny},
	}})
	if err == nil || !strings.Contains(err.Error(), "must start with '/'") {
		t.Fatalf("err = %v", err)
	}
}
