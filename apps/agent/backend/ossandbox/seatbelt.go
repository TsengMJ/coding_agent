package ossandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// SandboxExecPath is macOS's Seatbelt launcher.
const SandboxExecPath = "/usr/bin/sandbox-exec"

// seatbeltMachServices are the system services sandboxed commands may talk
// to: user and group lookup, logging, preferences, temp-dir lookup and
// certificate verification. Launch Services and Apple Events are absent,
// so `open` and `osascript` cannot start programs outside the sandbox.
var seatbeltMachServices = []string{
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.system.DirectoryService.libinfo_v1",
	"com.apple.system.logger",
	"com.apple.system.notification_center",
	"com.apple.logd",
	"com.apple.diagnosticd",
	"com.apple.cfprefsd.daemon",
	"com.apple.cfprefsd.agent",
	"com.apple.bsd.dirhelper",
	"com.apple.trustd",
	"com.apple.trustd.agent",
	"com.apple.SecurityServer",
	"com.apple.ocspd",
	"com.apple.analyticsd",
	"com.apple.sysmond",
	"com.apple.FSEvents",
}

// seatbeltBase denies everything, then allows what ordinary build and
// test commands need besides file writes and the network.
const seatbeltBase = `(version 1)
(deny default)
(allow process-exec)
(allow process-fork)
(allow process-info* (target same-sandbox))
(allow signal (target same-sandbox))
(allow sysctl-read)
(allow user-preference-read)
(allow ipc-posix-sem)
(allow ipc-posix-shm)
(allow pseudo-tty)
(allow file-ioctl)
(allow file-read*)
(allow file-write*
  (literal "/dev/null")
  (literal "/dev/zero")
  (literal "/dev/tty")
  (literal "/dev/ptmx")
  (literal "/dev/dtracehelper")
  (regex #"^/dev/fd/[0-9]+$")
  (regex #"^/dev/ttys[0-9]+$"))
`

// SeatbeltProfile renders p as a Seatbelt profile. Later rules win, so
// writable roots come before the deny rules that carve holes in them.
func SeatbeltProfile(p Policy) string {
	var b strings.Builder
	b.WriteString(seatbeltBase)
	b.WriteString("(allow mach-lookup")
	for _, s := range seatbeltMachServices {
		fmt.Fprintf(&b, "\n  (global-name %s)", sbString(s))
	}
	b.WriteString(")\n")
	if p.Mode == WorkspaceWrite {
		writeRule(&b, "(allow file-write*", expand(p.WritableRoots))
	}
	writeRule(&b, "(deny file-write*", expand(p.DenyWrite))
	writeRule(&b, "(deny file-read* file-write*", expand(p.DenyRead))
	if p.Network == NetworkFull {
		b.WriteString("(allow network*)\n(allow system-socket)\n(allow mach-lookup (global-name \"com.apple.dnssd.service\"))\n")
	} else {
		b.WriteString(localhostNetwork)
	}
	return b.String()
}

// localhostNetwork lets tests bind and reach loopback servers (and, with
// NetworkAllowlist, the proxy listening there); nothing else on the
// network is reachable.
const localhostNetwork = `(allow network-bind (local ip "localhost:*"))
(allow network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`

func writeRule(b *strings.Builder, head string, paths []string) {
	if len(paths) == 0 {
		return
	}
	b.WriteString(head)
	for _, p := range paths {
		fmt.Fprintf(b, "\n  (subpath %s)", sbString(p))
	}
	b.WriteString(")\n")
}

// sbString quotes s as a Seatbelt string literal.
func sbString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Seatbelt confines commands with sandbox-exec.
type Seatbelt struct{}

func (Seatbelt) Name() string { return "seatbelt" }

func (Seatbelt) Command(ctx context.Context, p Policy, shellCmd string) (*exec.Cmd, error) {
	if p.Mode == FullAccess {
		return None().Command(ctx, p, shellCmd)
	}
	if err := checkProxy(p); err != nil {
		return nil, err
	}
	if _, err := os.Stat(SandboxExecPath); err != nil {
		return nil, fmt.Errorf("sandbox: %s not found: %w", SandboxExecPath, err)
	}
	return exec.CommandContext(ctx, SandboxExecPath, "-p", SeatbeltProfile(p), "sh", "-c", shellCmd), nil
}
