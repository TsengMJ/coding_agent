// Package ossandbox runs shell commands inside an OS-level process sandbox:
// Seatbelt (sandbox-exec) on macOS and bubblewrap on Linux. Only the shell
// child is confined; the agent process itself is not.
package ossandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ITEnvVar set to 1 enables integration tests that run real sandboxed
// commands (make test-sandbox, make test-sandbox-linux).
const ITEnvVar = "QUILL_SANDBOX_IT"

// Mode is the filesystem access a sandboxed command gets.
type Mode string

const (
	// ReadOnly lets commands read the machine but write nothing except
	// device files such as /dev/null.
	ReadOnly Mode = "read-only"
	// WorkspaceWrite also lets commands write Policy.WritableRoots.
	WorkspaceWrite Mode = "workspace-write"
	// FullAccess runs commands without a sandbox.
	FullAccess Mode = "full-access"
)

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.TrimSpace(s)); m {
	case ReadOnly, WorkspaceWrite, FullAccess:
		return m, nil
	}
	return "", fmt.Errorf("sandbox mode %q: want %s, %s or %s", s, ReadOnly, WorkspaceWrite, FullAccess)
}

// Network is the network access a sandboxed command gets.
type Network string

const (
	// NetworkNone blocks every connection except to localhost.
	NetworkNone Network = "none"
	// NetworkAllowlist routes traffic through Policy.Proxy, which only
	// forwards to allowed domains.
	NetworkAllowlist Network = "allowlist"
	// NetworkFull allows every connection.
	NetworkFull Network = "full"
)

// ParseNetwork validates a network policy name.
func ParseNetwork(s string) (Network, error) {
	switch n := Network(strings.TrimSpace(s)); n {
	case NetworkNone, NetworkAllowlist, NetworkFull:
		return n, nil
	}
	return "", fmt.Errorf("sandbox network %q: want %s, %s or %s", s, NetworkNone, NetworkAllowlist, NetworkFull)
}

// Policy describes what a sandboxed command may do. Paths are absolute.
type Policy struct {
	Mode Mode
	// WritableRoots are writable in WorkspaceWrite mode, ignored otherwise.
	WritableRoots []string
	// DenyWrite stays read-only even inside a writable root.
	DenyWrite []string
	// DenyRead can be neither read nor written.
	DenyRead []string
	Network  Network
	// Proxy is the allowlist proxy for NetworkAllowlist.
	Proxy ProxyEndpoint
}

// ProxyEndpoint is where sandboxed commands reach the allowlist proxy.
type ProxyEndpoint struct {
	// Port is the proxy's loopback TCP port (Seatbelt connects to it directly).
	Port int
	// Socket is a unix socket serving the same proxy; bubblewrap bind-mounts
	// it into the sandbox's network namespace.
	Socket string
}

// Sandbox wraps shell commands in an OS sandbox.
type Sandbox interface {
	Name() string
	// Command returns a command that runs `sh -c shellCmd` under p.
	Command(ctx context.Context, p Policy, shellCmd string) (*exec.Cmd, error)
}

// None runs commands without a sandbox.
func None() Sandbox { return none{} }

type none struct{}

func (none) Name() string { return "none" }

func (none) Command(ctx context.Context, _ Policy, shellCmd string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "sh", "-c", shellCmd), nil
}

// DefaultPolicy is the policy for a project rooted at root: in
// WorkspaceWrite mode the project, temp and toolchain cache directories are
// writable; files that would let a command run code outside the sandbox
// later (.git/hooks, .git/config, project .quill settings) are not;
// credential directories cannot be read; the network is off.
func DefaultPolicy(mode Mode, root string) Policy {
	p := Policy{Mode: mode, Network: NetworkNone, DenyRead: SecretPaths()}
	if mode == WorkspaceWrite {
		p.WritableRoots = append([]string{root}, append(TempDirs(), CacheDirs()...)...)
	}
	p.DenyWrite = []string{
		filepath.Join(root, ".git", "hooks"),
		filepath.Join(root, ".git", "config"),
		filepath.Join(root, ".quill"),
	}
	return p
}

// SecretPaths are credential files and directories under the home
// directory that sandboxed commands cannot read.
func SecretPaths() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	rel := []string{
		".ssh", ".aws", ".gnupg", ".kube", ".netrc", ".docker/config.json",
		".config/gh", ".config/gcloud", ".azure", ".quill/.env", ".git-credentials",
	}
	out := make([]string, 0, len(rel)+2)
	for _, r := range rel {
		out = append(out, filepath.Join(home, filepath.FromSlash(r)))
	}
	// The docker socket controls a root daemon; reaching it escapes any sandbox.
	return append(out, "/var/run/docker.sock", "/run/docker.sock", filepath.Join(home, ".docker", "run"))
}

// TempDirs are the system temp directories.
func TempDirs() []string {
	dirs := []string{"/tmp"}
	if t := os.TempDir(); t != "" && t != "/tmp" {
		dirs = append(dirs, t)
		// macOS per-user temp is .../T; its siblings C (cache) and 0 are
		// used by compilers and confstr(_CS_DARWIN_USER_*_DIR).
		if filepath.Base(filepath.Clean(t)) == "T" {
			dirs = append(dirs, filepath.Dir(filepath.Clean(t)))
		}
	}
	return dirs
}

// CacheDirs are the user-level caches build tools write to (Go build and
// module caches, npm, pip, ...).
func CacheDirs() []string {
	var dirs []string
	if d, err := os.UserCacheDir(); err == nil {
		dirs = append(dirs, d)
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".cache"), filepath.Join(home, ".npm"))
	}
	for _, env := range []string{"GOCACHE", "GOMODCACHE"} {
		if v := os.Getenv(env); filepath.IsAbs(v) {
			dirs = append(dirs, v)
		}
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" && home != "" {
		gopath = filepath.Join(home, "go")
	}
	for _, p := range filepath.SplitList(gopath) {
		if filepath.IsAbs(p) {
			dirs = append(dirs, filepath.Join(p, "pkg", "mod"))
		}
	}
	return dedupe(dirs)
}

// ShellJoin quotes args into one sh command line.
func ShellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// canonical returns p and, when different, p with symlinks resolved. A
// missing path is resolved through its deepest existing parent, so a rule
// for a not-yet-created file still matches once it exists.
func canonical(p string) []string {
	p = filepath.Clean(p)
	resolved := resolve(p)
	if resolved == p {
		return []string{p}
	}
	return []string{p, resolved}
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	parent := filepath.Dir(p)
	if parent == p {
		return p
	}
	return filepath.Join(resolve(parent), filepath.Base(p))
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	out := items[:0:0]
	for _, s := range items {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// expand canonicalizes and dedupes paths.
func expand(paths []string) []string {
	var out []string
	for _, p := range paths {
		out = append(out, canonical(p)...)
	}
	return dedupe(out)
}
