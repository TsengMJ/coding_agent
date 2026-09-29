package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// localContextTimeout bounds every git command so a slow or locked repo
// cannot delay agent startup.
const localContextTimeout = 2 * time.Second

// maxStatusEntries caps the `git status` lines shown in the prompt.
const maxStatusEntries = 20

// projectMarkers are top-level files that identify the toolchain.
var projectMarkers = []string{"go.mod", "package.json", "pyproject.toml", "Cargo.toml", "Makefile", "AGENTS.md"}

// LocalContext renders the "Project Context" prompt section for cwd: git
// branch, uncommitted changes and project marker files
// (Python `local_context.py`, reduced). It returns "" when nothing is known.
func LocalContext(ctx context.Context, cwd string) string {
	var lines []string
	if branch, ok := gitBranch(ctx, cwd); ok {
		lines = append(lines, "- Git branch: "+branch)
		if status, ok := git(ctx, cwd, "status", "--porcelain"); ok {
			lines = append(lines, statusLines(status)...)
		}
	}
	var markers []string
	for _, name := range projectMarkers {
		if fi, err := os.Stat(filepath.Join(cwd, name)); err == nil && !fi.IsDir() {
			markers = append(markers, name)
		}
	}
	if len(markers) > 0 {
		lines = append(lines, "- Project files: "+strings.Join(markers, ", "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "### Project Context\n\n" + strings.Join(lines, "\n") + "\n\n"
}

func statusLines(status string) []string {
	status = strings.TrimRight(status, "\n")
	if status == "" {
		return []string{"- Working tree: clean"}
	}
	entries := strings.Split(status, "\n")
	out := []string{fmt.Sprintf("- Uncommitted changes (%d):", len(entries)), "  ```"}
	for _, e := range entries[:min(len(entries), maxStatusEntries)] {
		out = append(out, "  "+e)
	}
	if len(entries) > maxStatusEntries {
		out = append(out, fmt.Sprintf("  ... %d more", len(entries)-maxStatusEntries))
	}
	return append(out, "  ```")
}

// gitBranch names the checked-out branch (also before the first commit) or
// the detached commit; ok is false outside a git work tree.
func gitBranch(ctx context.Context, cwd string) (string, bool) {
	if name, ok := git(ctx, cwd, "symbolic-ref", "--short", "-q", "HEAD"); ok {
		return fmt.Sprintf("`%s`", strings.TrimSpace(name)), true
	}
	if sha, ok := git(ctx, cwd, "rev-parse", "--short", "HEAD"); ok {
		return fmt.Sprintf("detached at `%s`", strings.TrimSpace(sha)), true
	}
	return "", false
}

func git(ctx context.Context, cwd string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, localContextTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}
