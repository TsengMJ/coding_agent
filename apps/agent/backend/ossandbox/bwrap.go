package ossandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	// Bridge is the binary that handles NetBridgeArg; defaults to the
	// running executable.
	Bridge string
}

func (Bwrap) Name() string { return "bwrap" }

func (b Bwrap) Command(ctx context.Context, p Policy, shellCmd string) (*exec.Cmd, error) {
	if p.Mode == FullAccess {
		return None().Command(ctx, p, shellCmd)
	}
	if err := checkProxy(p); err != nil {
		return nil, err
	}
	if p.Mode == WorkspaceWrite {
		// bwrap can only bind existing paths, so a cache directory a tool
		// would create on first use (e.g. $GOPATH/pkg/sumdb) is made now;
		// one that cannot be created simply stays read-only.
		for _, r := range p.WritableRoots {
			_ = os.MkdirAll(r, 0o755)
		}
	}
	args := append(BwrapArgs(p), "--")
	if p.Network == NetworkAllowlist {
		// The network namespace has no route to the host's proxy port; the
		// bridge relays a loopback port to the proxy's unix socket.
		if p.Proxy.Socket == "" {
			return nil, errors.New("sandbox: network allowlist needs the proxy's unix socket under bubblewrap")
		}
		bridge := b.Bridge
		if bridge == "" {
			exe, err := os.Executable()
			if err != nil {
				return nil, fmt.Errorf("sandbox: locate quill for the network bridge: %w", err)
			}
			bridge = exe
		}
		args = append(args, bridge, NetBridgeArg, strconv.Itoa(p.Proxy.Port), p.Proxy.Socket)
	}
	args = append(args, "sh", "-c", shellCmd)
	return exec.CommandContext(ctx, b.Path, args...), nil
}
