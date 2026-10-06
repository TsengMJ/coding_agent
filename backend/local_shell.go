package backend

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tsengmingchun/quill/backend/ossandbox"
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
	// HideSecretEnv leaves inherited variables whose names look like
	// credentials (SecretEnvName) out of the command environment, except
	// those named in PassEnv. Env is always passed.
	HideSecretEnv bool
	PassEnv       []string
	// Sandbox confines commands to Policy; nil runs them unconfined.
	Sandbox ossandbox.Sandbox
	Policy  ossandbox.Policy
}

// LocalShellBackend is a FilesystemBackend that also runs shell commands on
// the host. Without a Sandbox it provides no isolation: commands can touch
// any path regardless of VirtualMode.
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

	cmd, err := b.command(runCtx, command)
	if err != nil {
		return errorResponse(fmt.Sprintf("Error executing command: %v", err), 1)
	}
	cmd.Dir = b.root
	cmd.Env = b.env()
	if b.opts.Sandbox != nil && !sandboxBypassed(ctx) {
		cmd.Env = append(cmd.Env, b.opts.Policy.ProxyEnv()...)
	}
	stdout := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	stderr := &cappedBuffer{limit: b.opts.MaxOutputBytes}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	setProcessGroup(cmd)
	err = cmd.Run()
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
	return renderOutput(stdout, stderr, exitCode, b.opts.MaxOutputBytes)
}

// renderOutput formats a finished command: stdout, then stderr lines
// prefixed with "[stderr] ", then a non-zero exit code. Output over limit
// bytes keeps its head and tail: test summaries and errors come last.
func renderOutput(stdout, stderr *cappedBuffer, exitCode, limit int) ExecuteResponse {
	var parts []string
	out, outOmitted := stdout.render()
	if out != "" {
		parts = append(parts, out)
	}
	errText, errOmitted := stderr.render()
	if s := strings.TrimSpace(errText); s != "" {
		for _, line := range strings.Split(s, "\n") {
			parts = append(parts, "[stderr] "+line)
		}
	}
	output := strings.Join(parts, "\n")
	if output == "" {
		output = "<no output>"
	}
	omitted := outOmitted + errOmitted
	if visibleLen(output) > limit {
		head, tail := headTailLines(output, limit)
		omitted += int64(visibleLen(output) - visibleLen(head) - visibleLen(tail))
		output = head + omittedGap + tail
	}
	truncated := omitted > 0
	if truncated {
		output = fmt.Sprintf("%s\n\n... Output truncated at %d bytes: %d bytes were omitted where marked %q; rerun through grep, head or tail to see them.",
			strings.TrimRight(output, " \t\n"), limit, omitted, strings.TrimSpace(omittedGap))
	}
	if exitCode != 0 {
		output = fmt.Sprintf("%s\n\nExit code: %d", strings.TrimRight(output, " \t\n"), exitCode)
	}
	return ExecuteResponse{Output: output, ExitCode: &exitCode, Truncated: truncated}
}

// omittedGap marks where truncated output was cut.
const omittedGap = "\n[... output omitted ...]\n"

// cappedBuffer keeps the first and last limit/2 bytes written and counts
// the rest, so a chatty command cannot grow memory without bound and its
// final lines survive.
type cappedBuffer struct {
	head, tail []byte
	limit      int
	omitted    int64
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.limit/2 - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head = append(c.head, p[:k]...)
		p = p[k:]
	}
	c.tail = append(c.tail, p...)
	// Trimming only once the tail doubles keeps copying amortized.
	if len(c.tail) >= 2*c.tailCap() {
		c.trimTail()
	}
	return n, nil
}

func (c *cappedBuffer) tailCap() int { return max(c.limit-c.limit/2, 1) }

func (c *cappedBuffer) trimTail() {
	if over := len(c.tail) - c.tailCap(); over > 0 {
		c.omitted += int64(over)
		c.tail = append(c.tail[:0], c.tail[over:]...)
	}
}

// render returns the kept bytes, with omittedGap where bytes were left
// out, and how many were; runes cut at either edge of the gap are dropped.
func (c *cappedBuffer) render() (string, int64) {
	c.trimTail()
	if c.omitted == 0 {
		return string(c.head) + string(c.tail), 0
	}
	head, tail := trimCutRune(string(c.head)), trimLeadingCutRune(string(c.tail))
	return head + omittedGap + tail, c.omitted + int64(len(c.head)-len(head)+len(c.tail)-len(tail))
}

// visibleLen is len(s) without the gap markers, which do not count
// against the output limit.
func visibleLen(s string) int {
	return len(s) - strings.Count(s, omittedGap)*len(omittedGap)
}

// headTailLines returns about the first and last n/2 bytes of s, cut after
// a newline when the half has one (so a gap marker is never split) and
// otherwise on a rune boundary.
func headTailLines(s string, n int) (head, tail string) {
	h := min(n/2, len(s))
	if i := strings.LastIndexByte(s[:h], '\n'); i >= 0 {
		h = i + 1
	}
	for h > 0 && h < len(s) && !utf8.RuneStart(s[h]) {
		h--
	}
	t := max(len(s)-(n-n/2), h)
	if i := strings.IndexByte(s[t:], '\n'); i >= 0 && t+i+1 < len(s) {
		t += i + 1
	}
	for t < len(s) && !utf8.RuneStart(s[t]) {
		t++
	}
	return s[:h], s[t:]
}

func trimCutRune(s string) string {
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

func trimLeadingCutRune(s string) string {
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

func errorResponse(msg string, code int) ExecuteResponse {
	return ExecuteResponse{Output: msg, ExitCode: &code}
}

type withoutSandboxKey struct{}

// WithoutSandbox marks ctx so a sandboxed LocalShellBackend runs the
// command unconfined. Callers must have had the command approved.
func WithoutSandbox(ctx context.Context) context.Context {
	return context.WithValue(ctx, withoutSandboxKey{}, true)
}

func sandboxBypassed(ctx context.Context) bool {
	v, _ := ctx.Value(withoutSandboxKey{}).(bool)
	return v
}

func (b *LocalShellBackend) command(ctx context.Context, command string) (*exec.Cmd, error) {
	if b.opts.Sandbox == nil || sandboxBypassed(ctx) {
		return exec.CommandContext(ctx, "sh", "-c", command), nil
	}
	return b.opts.Sandbox.Command(ctx, b.opts.Policy, command)
}

// Sandboxed reports whether commands run inside an OS sandbox.
func (b *LocalShellBackend) Sandboxed() bool {
	return ossandbox.Confines(b.opts.Sandbox, b.opts.Policy)
}

// SandboxPolicy returns the policy commands run under; meaningful only
// when Sandboxed.
func (b *LocalShellBackend) SandboxPolicy() ossandbox.Policy { return b.opts.Policy }

func (b *LocalShellBackend) env() []string {
	var env []string
	if b.opts.InheritEnv {
		for _, kv := range os.Environ() {
			name, _, _ := strings.Cut(kv, "=")
			if b.opts.HideSecretEnv && SecretEnvName(name) && !slices.Contains(b.opts.PassEnv, name) {
				continue
			}
			env = append(env, kv)
		}
	}
	for k, v := range b.opts.Env {
		env = append(env, k+"="+v)
	}
	if env == nil {
		env = []string{}
	}
	return env
}
