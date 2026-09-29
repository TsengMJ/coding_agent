package backend

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultExecuteTimeout is LocalShellBackend's default command timeout.
const DefaultExecuteTimeout = 120 * time.Second

// DefaultMaxOutputBytes caps combined command output.
const DefaultMaxOutputBytes = 100_000

// timeoutExitCode is the conventional exit code reported for timed-out commands.
const timeoutExitCode = 124

// LocalShellOptions configures LocalShellBackend.
type LocalShellOptions struct {
	FilesystemOptions
	// Timeout defaults to DefaultExecuteTimeout.
	Timeout time.Duration
	// MaxOutputBytes defaults to DefaultMaxOutputBytes.
	MaxOutputBytes int
	Env            map[string]string
	// InheritEnv passes the parent process environment to commands.
	InheritEnv bool
}

// LocalShellBackend is a FilesystemBackend that also runs shell commands on
// the host. It provides no isolation: commands can touch any path regardless
// of VirtualMode.
type LocalShellBackend struct {
	*FilesystemBackend
	opts LocalShellOptions
	id   string
}

// NewLocalShellBackend returns a host shell backend.
func NewLocalShellBackend(opts LocalShellOptions) *LocalShellBackend {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultExecuteTimeout
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return &LocalShellBackend{
		FilesystemBackend: NewFilesystemBackend(opts.FilesystemOptions),
		opts:              opts,
		id:                "local-" + hex.EncodeToString(suffix),
	}
}

// ID identifies this backend instance.
func (b *LocalShellBackend) ID() string { return b.id }

// Execute runs command with `sh -c` in the root directory. Stdout comes
// first, then stderr lines prefixed with "[stderr] ". A non-zero exit code
// is appended to the output.
func (b *LocalShellBackend) Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse {
	if strings.TrimSpace(command) == "" {
		return errorResponse("Error: Command must be a non-empty string.", 1)
	}
	if b.rootErr != nil {
		return errorResponse(fmt.Sprintf("Error executing command: %v", b.rootErr), 1)
	}
	effective := b.opts.Timeout
	if timeout != nil {
		effective = *timeout
	}
	if effective <= 0 {
		return errorResponse(fmt.Sprintf("Error: timeout must be positive, got %s", effective), 1)
	}
	runCtx, cancel := context.WithTimeout(ctx, effective)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = b.root
	cmd.Env = b.env()
	stdout := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	stderr := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	setProcessGroup(cmd)
	err := cmd.Run()
	// Background children (`cmd &`) would otherwise outlive the tool call.
	_ = killProcessGroup(cmd)

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		msg := fmt.Sprintf("Error: Command timed out after %s. For long-running commands, re-run using the timeout parameter.", effective)
		if timeout != nil {
			msg = fmt.Sprintf("Error: Command timed out after %s (custom timeout). The command may be stuck or require more time.", effective)
		}
		return errorResponse(msg, timeoutExitCode)
	}
	exitCode := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		exitCode = exitErr.ExitCode()
	case errors.Is(err, exec.ErrWaitDelay):
		// The shell exited but a background child kept the output pipes open.
	case err != nil:
		return errorResponse(fmt.Sprintf("Error executing command: %v", err), 1)
	}

	var parts []string
	if stdout.buf.Len() > 0 {
		parts = append(parts, stdout.String())
	}
	if stderr.buf.Len() > 0 {
		for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
			parts = append(parts, "[stderr] "+line)
		}
	}
	output := strings.Join(parts, "\n")
	if output == "" {
		output = "<no output>"
	}
	truncated := stdout.dropped > 0 || stderr.dropped > 0
	if len(output) > b.opts.MaxOutputBytes {
		output = truncateUTF8(output, b.opts.MaxOutputBytes)
		truncated = true
	}
	if truncated {
		output += fmt.Sprintf("\n\n... Output truncated at %d bytes.", b.opts.MaxOutputBytes)
	}
	if exitCode != 0 {
		output = fmt.Sprintf("%s\n\nExit code: %d", strings.TrimRight(output, " \t\n"), exitCode)
	}
	return ExecuteResponse{Output: output, ExitCode: &exitCode, Truncated: truncated}
}

// cappedBuffer keeps the first limit bytes written and counts the rest, so a
// chatty command cannot grow memory without bound.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int64
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	keep := min(max(c.limit-c.buf.Len(), 0), len(p))
	c.buf.Write(p[:keep])
	c.dropped += int64(len(p) - keep)
	return len(p), nil
}

// String returns the kept bytes, minus a rune the limit cut in half.
func (c *cappedBuffer) String() string {
	s := c.buf.String()
	if c.dropped == 0 {
		return s
	}
	for i := len(s) - 1; i >= 0 && i >= len(s)-utf8.UTFMax; i-- {
		if utf8.RuneStart(s[i]) {
			if !utf8.FullRuneInString(s[i:]) {
				return s[:i]
			}
			break
		}
	}
	return s
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func errorResponse(msg string, code int) ExecuteResponse {
	return ExecuteResponse{Output: msg, ExitCode: &code}
}

func (b *LocalShellBackend) env() []string {
	var env []string
	if b.opts.InheritEnv {
		env = os.Environ()
	}
	for k, v := range b.opts.Env {
		env = append(env, k+"="+v)
	}
	if env == nil {
		env = []string{}
	}
	return env
}
