// Package delivery turns a thread's changes into something a reviewer can
// use: a diff of the working tree, and a branch pushed to GitHub with a pull
// request authored by the user.
//
// A workspace repository is under the agent's control, including its
// .git/config (core.fsmonitor, filters, url.insteadOf), so git never runs on
// it from the server host. Diffs and commits run in the workspace container;
// pushes run from a fresh repository on the host that receives the commits
// as a bundle file.
package delivery

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
)

// Git runs git in one repository and returns its stdout.
type Git interface {
	Run(ctx context.Context, env []string, args ...string) ([]byte, error)
}

// HostGit runs git on this host. Use it only for repositories the user
// trusts (the local single-user server's working directory).
type HostGit struct{ Dir string }

func (g HostGit) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ContainerGit runs git in a workspace container, at workspace.ContainerDir.
type ContainerGit struct {
	Docker    workspace.Docker
	Container string
}

func (g ContainerGit) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	cmd := append([]string{"env"}, env...)
	cmd = append(append(cmd, "git"), args...)
	out, err := g.Docker.Output(ctx, g.Container, workspace.ContainerDir, cmd...)
	if err != nil {
		return out, fmt.Errorf("git %s in %s: %w", args[0], g.Container, err)
	}
	return out, nil
}

func trimmed(out []byte, err error) (string, error) {
	return strings.TrimSpace(string(out)), err
}
