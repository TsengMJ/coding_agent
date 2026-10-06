package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/backend/ossandbox"
)

func TestSandboxDefaults(t *testing.T) {
	clearEnv(t)
	s := resolve(t, writeConfig(t, "")).Sandbox
	if s.Mode != DefaultSandboxMode || s.Network != ossandbox.NetworkNone || s.OnUnavailable != SandboxUnavailableError || !s.AllowUnsandboxed {
		t.Fatalf("defaults = %+v", s)
	}
}

func TestDefaultSandboxModeIsOnWhereSupported(t *testing.T) {
	for goos, want := range map[string]ossandbox.Mode{
		"darwin":  ossandbox.WorkspaceWrite,
		"linux":   ossandbox.WorkspaceWrite,
		"windows": ossandbox.FullAccess,
	} {
		if got := defaultSandboxMode(goos); got != want {
			t.Errorf("defaultSandboxMode(%s) = %s, want %s", goos, got, want)
		}
	}
}

func TestSandboxFromConfig(t *testing.T) {
	clearEnv(t)
	home, _ := os.UserHomeDir()
	s := resolve(t, writeConfig(t, `[sandbox]
mode = "read-only"
network = "allowlist"
allowed_domains = ["Example.com", " "]
writable_roots = ["~/scratch", "/opt/cache"]
deny_read = ["~/.secrets"]
on_unavailable = "warn"
allow_unsandboxed = false
`)).Sandbox
	if s.Mode != ossandbox.ReadOnly || s.Network != ossandbox.NetworkAllowlist || s.OnUnavailable != SandboxUnavailableWarn || s.AllowUnsandboxed {
		t.Fatalf("settings = %+v", s)
	}
	if len(s.AllowedDomains) != 1 || s.AllowedDomains[0] != "example.com" {
		t.Errorf("AllowedDomains = %v", s.AllowedDomains)
	}
	if len(s.WritableRoots) != 2 || s.WritableRoots[0] != filepath.Join(home, "scratch") || s.WritableRoots[1] != "/opt/cache" {
		t.Errorf("WritableRoots = %v", s.WritableRoots)
	}
	if len(s.DenyRead) != 1 || s.DenyRead[0] != filepath.Join(home, ".secrets") {
		t.Errorf("DenyRead = %v", s.DenyRead)
	}
}

func TestSandboxEnvOverridesMode(t *testing.T) {
	clearEnv(t)
	t.Setenv(SandboxModeEnvVar, "workspace-write")
	if s := resolve(t, writeConfig(t, "[sandbox]\nmode = \"read-only\"\n")).Sandbox; s.Mode != ossandbox.WorkspaceWrite {
		t.Fatalf("Mode = %q", s.Mode)
	}
}

func TestSandboxRejectsBadValues(t *testing.T) {
	clearEnv(t)
	for _, body := range []string{
		"[sandbox]\nmode = \"danger\"\n",
		"[sandbox]\nnetwork = \"some\"\n",
		"[sandbox]\non_unavailable = \"ignore\"\n",
		"[sandbox]\nwritable_roots = [\"relative/dir\"]\n",
	} {
		_, err := NewResolver(writeConfig(t, body))
		if err == nil || !strings.Contains(err.Error(), "sandbox.") {
			t.Errorf("%q: err = %v", body, err)
		}
	}
}

func TestSandboxEnabled(t *testing.T) {
	for mode, want := range map[ossandbox.Mode]bool{"": false, ossandbox.FullAccess: false, ossandbox.ReadOnly: true, ossandbox.WorkspaceWrite: true} {
		if got := (SandboxSettings{Mode: mode}).Enabled(); got != want {
			t.Errorf("Enabled(%q) = %v", mode, got)
		}
	}
}
