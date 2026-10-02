package codetools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
)

// execResult is a backend command split back into its streams.
type execResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Truncated bool
}

var truncationNote = regexp.MustCompile(`\n\n\.\.\. Output truncated at \d+ bytes[^\n]*$`)

// run executes command through sb and undoes the LocalShellBackend output
// format (stdout, "[stderr] " lines, truncation note, "Exit code: N").
func run(ctx context.Context, sb backend.SandboxBackend, command string, timeout time.Duration) execResult {
	resp := sb.Execute(ctx, command, &timeout)
	return splitExecOutput(resp)
}

func splitExecOutput(resp backend.ExecuteResponse) execResult {
	res := execResult{ExitCode: -1, Truncated: resp.Truncated}
	out := resp.Output
	if resp.ExitCode != nil {
		res.ExitCode = *resp.ExitCode
		if res.ExitCode != 0 {
			out = strings.TrimSuffix(out, fmt.Sprintf("\n\nExit code: %d", res.ExitCode))
		}
	}
	if resp.Truncated {
		out = truncationNote.ReplaceAllString(out, "")
	}
	if out == "<no output>" {
		return res
	}
	lines := strings.Split(out, "\n")
	cut := len(lines)
	for cut > 0 && strings.HasPrefix(lines[cut-1], "[stderr] ") {
		cut--
	}
	var stderr []string
	for _, l := range lines[cut:] {
		stderr = append(stderr, strings.TrimPrefix(l, "[stderr] "))
	}
	res.Stdout = strings.Join(lines[:cut], "\n")
	res.Stderr = strings.Join(stderr, "\n")
	return res
}

// errText is the most useful message for a failed command.
func (r execResult) errText() string {
	if s := strings.TrimSpace(r.Stderr); s != "" {
		return s
	}
	return strings.TrimSpace(r.Stdout)
}

// shellQuote wraps s in single quotes for `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// projectPath turns a model-supplied path ("/a/b", "a/b", "./a") into a
// project-relative path, rejecting anything that leaves the project root.
func projectPath(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	p = strings.TrimLeft(p, "/")
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." {
		return ".", nil
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path %q must not contain '..'", raw)
		}
	}
	if strings.ContainsAny(p, "\x00\n") {
		return "", errors.New("path must not contain NUL or newline characters")
	}
	return strings.TrimSuffix(p, "/"), nil
}

// pathspec renders validated paths as a quoted git pathspec after "--".
func pathspec(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = shellQuote(p)
	}
	return " -- " + strings.Join(q, " ")
}
