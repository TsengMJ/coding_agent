package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/backend/ossandbox"
)

// SandboxModeEnvVar overrides sandbox.mode.
const SandboxModeEnvVar = "QUILL_SANDBOX_MODE"

// Sandbox fallbacks when the platform sandbox cannot run.
const (
	SandboxUnavailableError = "error"
	SandboxUnavailableWarn  = "warn"
)

// SandboxSettings is the [sandbox] table of config.toml.
type SandboxSettings struct {
	// Mode is read-only, workspace-write or full-access.
	Mode ossandbox.Mode
	// Network is none, allowlist or full.
	Network ossandbox.Network
	// AllowedDomains extend the allowlist proxy's defaults.
	AllowedDomains []string
	// WritableRoots and DenyRead extend the default policy; ~ expands.
	WritableRoots []string
	DenyRead      []string
	// OnUnavailable is "error" (refuse to start) or "warn" (run commands
	// unconfined and say so in every result).
	OnUnavailable string
	// AllowUnsandboxed lets the model ask, with approval, to run one
	// command outside the sandbox.
	AllowUnsandboxed bool
}

// DefaultAllowedDomains are reachable under sandbox.network = "allowlist":
// the Go module proxy and checksum database, GitHub, npm and PyPI.
var DefaultAllowedDomains = []string{
	"proxy.golang.org", "sum.golang.org", "github.com",
	"registry.npmjs.org", "pypi.org", "files.pythonhosted.org",
}

// AllowlistDomains are the defaults plus sandbox.allowed_domains.
func (s SandboxSettings) AllowlistDomains() []string {
	return append(slices.Clone(DefaultAllowedDomains), s.AllowedDomains...)
}

// DefaultSandboxMode applies when config.toml and the environment set
// none: workspace-write where quill has a sandbox (macOS, Linux), as in
// Codex and Cursor, and full-access elsewhere.
var DefaultSandboxMode = defaultSandboxMode(runtime.GOOS)

func defaultSandboxMode(goos string) ossandbox.Mode {
	if goos == "darwin" || goos == "linux" {
		return ossandbox.WorkspaceWrite
	}
	return ossandbox.FullAccess
}

func defaultSandboxSettings() SandboxSettings {
	return SandboxSettings{
		Mode:             DefaultSandboxMode,
		Network:          ossandbox.NetworkNone,
		OnUnavailable:    SandboxUnavailableError,
		AllowUnsandboxed: true,
	}
}

// Enabled reports whether shell commands run inside the OS sandbox.
func (s SandboxSettings) Enabled() bool { return s.Mode != ossandbox.FullAccess && s.Mode != "" }

type sandboxFile struct {
	Mode             string   `toml:"mode"`
	Network          string   `toml:"network"`
	AllowedDomains   []string `toml:"allowed_domains"`
	WritableRoots    []string `toml:"writable_roots"`
	DenyRead         []string `toml:"deny_read"`
	OnUnavailable    string   `toml:"on_unavailable"`
	AllowUnsandboxed *bool    `toml:"allow_unsandboxed"`
}

func applySandbox(s *SandboxSettings, f sandboxFile) error {
	if f.Mode != "" {
		m, err := ossandbox.ParseMode(f.Mode)
		if err != nil {
			return err
		}
		s.Mode = m
	}
	if f.Network != "" {
		n, err := ossandbox.ParseNetwork(f.Network)
		if err != nil {
			return err
		}
		s.Network = n
	}
	switch f.OnUnavailable {
	case "":
	case SandboxUnavailableError, SandboxUnavailableWarn:
		s.OnUnavailable = f.OnUnavailable
	default:
		return fmt.Errorf("on_unavailable: want %q or %q, got %q", SandboxUnavailableError, SandboxUnavailableWarn, f.OnUnavailable)
	}
	for _, list := range []struct {
		name string
		in   []string
		out  *[]string
	}{{"writable_roots", f.WritableRoots, &s.WritableRoots}, {"deny_read", f.DenyRead, &s.DenyRead}} {
		for _, p := range list.in {
			abs, err := expandHome(p)
			if err != nil {
				return fmt.Errorf("%s: %w", list.name, err)
			}
			*list.out = append(*list.out, abs)
		}
	}
	for _, d := range f.AllowedDomains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			s.AllowedDomains = append(s.AllowedDomains, d)
		}
	}
	if f.AllowUnsandboxed != nil {
		s.AllowUnsandboxed = *f.AllowUnsandboxed
	}
	return nil
}

// expandHome returns p with a leading ~ expanded; the result must be absolute.
func expandHome(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("path %q must be absolute or start with ~/", p)
	}
	return filepath.Clean(p), nil
}
