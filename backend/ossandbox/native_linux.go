//go:build linux

package ossandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	probeOnce sync.Once
	probed    Sandbox
	probeErr  error
)

// Native returns the platform sandbox, or an error saying why it cannot
// run. It checks once that bwrap exists and can create the namespaces it
// needs (unprivileged user namespaces may be disabled by the kernel or
// AppArmor).
func Native() (Sandbox, error) {
	probeOnce.Do(func() { probed, probeErr = probeBwrap() })
	return probed, probeErr
}

func probeBwrap() (Sandbox, error) {
	path, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("sandbox: bubblewrap (bwrap) not found: install it (e.g. apt install bubblewrap) or set sandbox.mode = %q", FullAccess)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append(BwrapArgs(Policy{Mode: ReadOnly, Network: NetworkNone}), "--", "true")
	cmd := exec.CommandContext(ctx, path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("sandbox: %s cannot create namespaces (%v: %s); enable unprivileged user namespaces "+
			"(sysctl kernel.unprivileged_userns_clone=1, or on Ubuntu kernel.apparmor_restrict_unprivileged_userns=0) "+
			"or set sandbox.mode = %q", path, err, strings.TrimSpace(stderr.String()), FullAccess)
	}
	return Bwrap{Path: path}, nil
}
