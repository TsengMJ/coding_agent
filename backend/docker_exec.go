package backend

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// dockerExecGrace lets the in-container timeout kill the command before the
// host gives up on the docker client.
var dockerExecGrace = 15 * time.Second

// inContainerTimeout runs "$2" under coreutils timeout (when the image has
// it) so a timed-out command is killed inside the container, not just the
// docker client on the host.
const inContainerTimeout = `if command -v timeout >/dev/null 2>&1; then exec timeout -k 5 "$1" sh -c "$2"; fi; exec sh -c "$2"`

// DockerExecOptions configures DockerExecBackend.
type DockerExecOptions struct {
	// FilesystemOptions.RootDir is the host directory bind-mounted at Workdir.
	FilesystemOptions
	Container string
	// Workdir is where commands run inside the container.
	Workdir string
	// Binary defaults to "docker".
	Binary string
	// Timeout defaults to DefaultExecuteTimeout.
	Timeout time.Duration
	// MaxOutputBytes defaults to DefaultMaxOutputBytes.
	MaxOutputBytes int
	Env            map[string]string
}

// DockerExecBackend serves file tools from a host directory and runs shell
// commands with `docker exec` in a container that mounts that directory.
type DockerExecBackend struct {
	*FilesystemBackend
	opts DockerExecOptions
}

// NewDockerExecBackend returns a backend for a running container.
func NewDockerExecBackend(opts DockerExecOptions) *DockerExecBackend {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultExecuteTimeout
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if opts.Binary == "" {
		opts.Binary = "docker"
	}
	return &DockerExecBackend{FilesystemBackend: NewFilesystemBackend(opts.FilesystemOptions), opts: opts}
}

// ID identifies the container.
func (b *DockerExecBackend) ID() string { return "docker-" + b.opts.Container }

// Execute runs command with `sh -c` in Workdir inside the container; output
// is formatted like LocalShellBackend's.
func (b *DockerExecBackend) Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse {
	if strings.TrimSpace(command) == "" {
		return errorResponse("Error: Command must be a non-empty string.", 1)
	}
	effective := b.opts.Timeout
	if timeout != nil {
		effective = *timeout
	}
	if effective <= 0 {
		return errorResponse(fmt.Sprintf("Error: timeout must be positive, got %s", effective), 1)
	}
	runCtx, cancel := context.WithTimeout(ctx, effective+dockerExecGrace)
	defer cancel()

	args := []string{"exec"}
	if b.opts.Workdir != "" {
		args = append(args, "-w", b.opts.Workdir)
	}
	for k, v := range b.opts.Env {
		args = append(args, "-e", k+"="+v)
	}
	secs := strconv.Itoa(int(max(effective.Round(time.Second), time.Second) / time.Second))
	args = append(args, b.opts.Container, "sh", "-c", inContainerTimeout, "sh", secs, command)
	cmd := exec.CommandContext(runCtx, b.opts.Binary, args...)
	stdout := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	stderr := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	start := time.Now()
	err := cmd.Run()

	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		exitCode = timeoutExitCode
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
	case err != nil:
		return errorResponse(fmt.Sprintf("Error executing command: %v", err), 1)
	}
	// timeout exits 124, or 137 when the -k kill was needed.
	if (exitCode == timeoutExitCode || exitCode == 137) && time.Since(start) >= effective {
		msg := fmt.Sprintf("Error: Command timed out after %s. For long-running commands, re-run using the timeout parameter.", effective)
		if timeout != nil {
			msg = fmt.Sprintf("Error: Command timed out after %s (custom timeout). The command may be stuck or require more time.", effective)
		}
		return errorResponse(msg, timeoutExitCode)
	}
	return renderOutput(stdout, stderr, exitCode, b.opts.MaxOutputBytes)
}
