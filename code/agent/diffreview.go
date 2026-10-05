package agent

import (
	"context"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/backend"
)

var (
	// byproductNames and byproductExts are files a run leaves behind (service
	// dumps, logs, editor and patch leftovers) rather than writes on purpose.
	byproductNames = []string{"dump.rdb", "nohup.out", ".DS_Store", "npm-debug.log", "yarn-error.log"}
	byproductExts  = []string{".rdb", ".log", ".pid", ".orig", ".rej", ".bak", ".swp", ".tmp", ".pyc"}
	byproductDirs  = []string{"__pycache__", ".pytest_cache", ".nyc_output"}

	testDirs     = []string{"test", "tests", "__tests__", "spec"}
	testFileName = regexp.MustCompile(`(?:_test\.go|^test_.*\.py|_test\.py|\.(?:test|spec)\.[cm]?[jt]sx?)$`)

	// requirementsHeading starts the task's requirement list (SWE-bench Pro
	// puts one under "## Requirements").
	requirementsHeading = regexp.MustCompile(`(?mi)^#{1,6}\s*requirements\s*:?\s*$`)
	nextHeading         = regexp.MustCompile(`(?m)^#{1,6}\s`)
)

const maxRequirements = 12

// reviewDiff checks the working tree once before a headless turn finishes:
// byproducts to delete, existing tests the agent changed, and the task's
// requirement list to check the change against. It returns "" when there
// is nothing to review.
func reviewDiff(ctx context.Context, sb backend.SandboxBackend, prompt string) string {
	var byproducts, tests []string
	t := gitTimeout
	resp := sb.Execute(ctx, "git status --porcelain=v1 -uall --no-renames", &t)
	if resp.ExitCode != nil && *resp.ExitCode == 0 {
		byproducts, tests = classifyStatus(resp.Output)
	}
	reqs := requirements(prompt)
	if len(byproducts) == 0 && len(tests) == 0 && len(reqs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(verifyMarker + " Before finishing, review your change (git_diff):")
	if len(byproducts) > 0 {
		fmt.Fprintf(&b, "\n- Delete files your work left behind that are not part of the change: %s. "+
			"Start services so they write outside the project (e.g. `redis-server --daemonize yes --save '' --dir /tmp`).", fileList(byproducts))
	}
	if len(tests) > 0 {
		fmt.Fprintf(&b, "\n- You changed existing tests: %s. Change an existing test only where the task changes the behaviour it checks; "+
			"never rewrite or weaken a test so that failing code passes. Revert the other test changes.", fileList(tests))
	}
	if len(reqs) > 0 {
		b.WriteString("\n- Check your change against each requirement of the task and fix any that is not met:")
		for i, r := range reqs {
			fmt.Fprintf(&b, "\n  %d. %s", i+1, clipLine(r, 400))
		}
	}
	b.WriteString("\nIf everything is already in order, finish without further changes.")
	return b.String()
}

// classifyStatus picks byproducts and changed existing test files out of
// `git status --porcelain` output.
func classifyStatus(out string) (byproducts, tests []string) {
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		code, p := line[:2], strings.Trim(line[3:], `"`)
		if isByproduct(p) {
			byproducts = append(byproducts, p)
			continue
		}
		if code != "??" && !strings.Contains(code, "A") && isTestFile(p) {
			tests = append(tests, p)
		}
	}
	return byproducts, tests
}

func isByproduct(p string) bool {
	base := path.Base(p)
	if slices.Contains(byproductNames, base) || slices.Contains(byproductExts, path.Ext(base)) {
		return true
	}
	for _, part := range strings.Split(path.Dir(p), "/") {
		if slices.Contains(byproductDirs, part) {
			return true
		}
	}
	return false
}

func isTestFile(p string) bool {
	if testFileName.MatchString(path.Base(p)) {
		return true
	}
	for _, part := range strings.Split(path.Dir(p), "/") {
		if slices.Contains(testDirs, part) {
			return true
		}
	}
	return false
}

// requirements reads the bullet list under the prompt's Requirements
// heading; a bullet may span several lines.
func requirements(prompt string) []string {
	loc := requirementsHeading.FindStringIndex(prompt)
	if loc == nil {
		return nil
	}
	section := prompt[loc[1]:]
	if end := nextHeading.FindStringIndex(section); end != nil {
		section = section[:end[0]]
	}
	var out []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			out = append(out, strings.TrimSpace(line[2:]))
		case len(out) > 0:
			out[len(out)-1] += " " + line
		}
	}
	if len(out) > maxRequirements {
		out = append(out[:maxRequirements], fmt.Sprintf("... and %d more in the task description", len(out)-maxRequirements))
	}
	return out
}
