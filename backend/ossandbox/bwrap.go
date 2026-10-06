package ossandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

// BwrapArgs renders p as bubblewrap arguments, before the `--` that starts
// the command. Later mounts shadow earlier ones, so the read-only root
// comes first, then writable roots, then the deny rules that carve holes
// in them. Missing paths are skipped: bwrap cannot mount over them.
func BwrapArgs(p Policy) []string {
	args := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--tmpfs", "/dev/shm",
		"--proc", "/proc",
		"--unshare-pid",
		"--unshare-ipc",
		"--die-with-parent",
		// A new session keeps commands from injecting input into the
		// controlling terminal (TIOCSTI).
		"--new-session",
	}
	if p.Network != NetworkFull {
		// The new network namespace has only its own loopback.
		args = append(args, "--unshare-net")
	}
	if p.Mode == WorkspaceWrite {
		for _, r := range existing(p.WritableRoots) {
			args = append(args, "--bind", r, r)
		}
	}
	for _, d := range existing(p.DenyWrite) {
		args = append(args, "--ro-bind", d, d)
	}
	for _, d := range existing(p.DenyRead) {
		if info, err := os.Stat(d); err == nil && info.IsDir() {
			args = append(args, "--tmpfs", d, "--remount-ro", d)
		} else {
			args = append(args, "--ro-bind", "/dev/null", d)
		}
	}
	return args
}

// existing resolves symlinks in paths and keeps those that exist.
func existing(paths []string) []string {
	var out []string
	for _, p := range paths {
		r, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	return dedupe(out)
}

// Bwrap confines commands with bubblewrap.
type Bwrap struct {
	// Path is the bwrap binary.
	Path string
}

func (Bwrap) Name() string { return "bwrap" }

func (b Bwrap) Command(ctx context.Context, p Policy, shellCmd string) (*exec.Cmd, error) {
	if p.Mode == FullAccess {
		return None().Command(ctx, p, shellCmd)
	}
	args := append(BwrapArgs(p), "--", "sh", "-c", shellCmd)
	return exec.CommandContext(ctx, b.Path, args...), nil
}
