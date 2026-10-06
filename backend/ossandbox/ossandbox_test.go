package ossandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, s := range []string{"read-only", "workspace-write", "full-access", " workspace-write "} {
		if _, err := ParseMode(s); err != nil {
			t.Errorf("ParseMode(%q) = %v", s, err)
		}
	}
	if _, err := ParseMode("danger"); err == nil {
		t.Error("ParseMode(danger) succeeded")
	}
}

func TestParseNetwork(t *testing.T) {
	if _, err := ParseNetwork("allowlist"); err != nil {
		t.Error(err)
	}
	if _, err := ParseNetwork("some"); err == nil {
		t.Error("ParseNetwork(some) succeeded")
	}
}

func TestDefaultPolicyWorkspaceWrite(t *testing.T) {
	p := DefaultPolicy(WorkspaceWrite, "/proj")
	if p.WritableRoots[0] != "/proj" {
		t.Fatalf("first writable root = %q, want /proj", p.WritableRoots[0])
	}
	if !slices.Contains(p.WritableRoots, "/tmp") {
		t.Errorf("writable roots %v lack /tmp", p.WritableRoots)
	}
	for _, want := range []string{"/proj/.git/hooks", "/proj/.git/config", "/proj/.quill"} {
		if !slices.Contains(p.DenyWrite, want) {
			t.Errorf("DenyWrite %v lacks %s", p.DenyWrite, want)
		}
	}
	if p.Network != NetworkNone {
		t.Errorf("Network = %q, want none", p.Network)
	}
}

func TestDefaultPolicyReadOnlyHasNoWritableRoots(t *testing.T) {
	if p := DefaultPolicy(ReadOnly, "/proj"); len(p.WritableRoots) != 0 {
		t.Fatalf("WritableRoots = %v", p.WritableRoots)
	}
}

func TestSecretPathsCoverCredentials(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	paths := SecretPaths()
	for _, want := range []string{"/home/u/.ssh", "/home/u/.aws", "/home/u/.quill/.env", "/var/run/docker.sock"} {
		if !slices.Contains(paths, want) {
			t.Errorf("SecretPaths() lacks %s", want)
		}
	}
}

func TestShellJoin(t *testing.T) {
	got := ShellJoin([]string{"echo", "a b", "it's", "x=1", ""})
	want := `echo 'a b' 'it'\''s' x=1 ''`
	if got != want {
		t.Fatalf("ShellJoin = %s, want %s", got, want)
	}
}

func TestCanonicalResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got := canonical(filepath.Join(link, "missing"))
	realDir, _ := filepath.EvalSymlinks(real)
	if len(got) != 2 || got[1] != filepath.Join(realDir, "missing") {
		t.Fatalf("canonical = %v, want the link path and %s", got, filepath.Join(realDir, "missing"))
	}
}

func TestSeatbeltProfileWorkspaceWrite(t *testing.T) {
	p := Policy{
		Mode:          WorkspaceWrite,
		WritableRoots: []string{"/nonexistent/ws"},
		DenyWrite:     []string{"/nonexistent/ws/.git/hooks"},
		DenyRead:      []string{"/nonexistent/home/.ssh"},
		Network:       NetworkNone,
	}
	prof := SeatbeltProfile(p)
	allow := strings.Index(prof, `(allow file-write*`+"\n"+`  (subpath "/nonexistent/ws")`)
	denyW := strings.Index(prof, `(deny file-write*`+"\n"+`  (subpath "/nonexistent/ws/.git/hooks")`)
	denyR := strings.Index(prof, `(deny file-read* file-write*`+"\n"+`  (subpath "/nonexistent/home/.ssh")`)
	if !strings.HasPrefix(prof, "(version 1)\n(deny default)") {
		t.Errorf("profile does not start with deny default:\n%s", prof)
	}
	if allow < 0 || denyW < 0 || denyR < 0 {
		t.Fatalf("missing rules:\n%s", prof)
	}
	if !(allow < denyW && denyW < denyR) {
		t.Errorf("deny rules must follow the writable roots they carve into:\n%s", prof)
	}
	if strings.Contains(prof, "(allow network*)") {
		t.Errorf("network none profile allows the network:\n%s", prof)
	}
}

func TestSeatbeltProfileReadOnlyIgnoresWritableRoots(t *testing.T) {
	prof := SeatbeltProfile(Policy{Mode: ReadOnly, WritableRoots: []string{"/nonexistent/ws"}})
	if strings.Contains(prof, `(subpath "/nonexistent/ws")`) {
		t.Fatalf("read-only profile grants a writable root:\n%s", prof)
	}
}

func TestSeatbeltProfileNetworkFull(t *testing.T) {
	if prof := SeatbeltProfile(Policy{Mode: ReadOnly, Network: NetworkFull}); !strings.Contains(prof, "(allow network*)") {
		t.Fatalf("network full profile does not allow the network:\n%s", prof)
	}
}

func TestSeatbeltProfileEscapesPaths(t *testing.T) {
	prof := SeatbeltProfile(Policy{Mode: WorkspaceWrite, WritableRoots: []string{`/nonexistent/a"b\c`}})
	if !strings.Contains(prof, `(subpath "/nonexistent/a\"b\\c")`) {
		t.Fatalf("path not escaped:\n%s", prof)
	}
}
