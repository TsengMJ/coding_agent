package codetools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	gitTimeout = 120 * time.Second
	// gitEnv keeps read-only git commands from taking the index lock.
	gitEnv = "export GIT_OPTIONAL_LOCKS=0 LC_ALL=C; "
	// gitSplit separates the outputs of two git commands in one Execute call.
	gitSplit = "__QUILL_GIT_SPLIT__"

	maxFileDiffBytes  = 16_000
	maxTotalDiffBytes = 64_000
	maxDiffFiles      = 300
	maxUntrackedDiffs = 50
)

const gitStatusDescription = `Show the git working tree state as JSON: branch, upstream, ahead/behind, and lists of modified, added (staged new), deleted, renamed, untracked and conflicted paths.

Paths are relative to the project root (prefix them with "/" for the file tools). Read-only; use it to see what you have changed before finishing a task.`

const gitDiffDescription = `Show uncommitted changes as JSON, one entry per file: {"files": [{"path", "old_path", "status", "additions", "deletions", "diff"}], "truncated"}.

- By default compares the working tree (staged and unstaged) with HEAD, and includes untracked files as additions.
- staged=true shows only staged changes.
- paths narrows the diff to files or directories (project-relative or "/"-prefixed).
- Large diffs are clipped per file and in total; narrow with paths to see the rest.
Read-only; prefer it over running "git diff" with execute.`

// GitDiffResult is the git_diff result.
type GitDiffResult struct {
	Files     []FileDiff `json:"files"`
	Truncated bool       `json:"truncated,omitempty"`
}

// FileDiff is one changed file.
type FileDiff struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	Diff      string `json:"diff"`
	// DiffOmitted is set when the total diff budget ran out before this file.
	DiffOmitted bool `json:"diff_omitted,omitempty"`
}

// NewGitStatus returns the git_status tool.
func NewGitStatus(sb backend.SandboxBackend) tool.Tool {
	return &tool.Func{
		ToolName:        GitStatusName,
		ToolEffect:      tool.ReadOnly,
		ToolDescription: gitStatusDescription,
		ToolSchema:      objectSchema(map[string]any{}),
		Fn: func(ctx context.Context, _ *tool.Runtime, _ map[string]any) (*tool.Result, error) {
			return gitStatus(ctx, sb), nil
		},
	}
}

func gitStatus(ctx context.Context, sb backend.SandboxBackend) *tool.Result {
	cmd := gitEnv + "git rev-parse --show-prefix && git -c core.quotepath=off status --porcelain=v2 -z --branch -- ."
	res := run(ctx, sb, cmd, gitTimeout)
	if res.ExitCode != 0 {
		return errorResult("Error: git status failed: %s", res.errText())
	}
	prefix, status, _ := strings.Cut(res.Stdout, "\n")
	st := parsePorcelainV2(status, strings.TrimSpace(prefix))
	st.Truncated = st.Truncated || res.Truncated
	return jsonResult(st)
}

// NewGitDiff returns the git_diff tool.
func NewGitDiff(sb backend.SandboxBackend) tool.Tool {
	return &tool.Func{
		ToolName:        GitDiffName,
		ToolEffect:      tool.ReadOnly,
		ToolDescription: gitDiffDescription,
		ToolSchema: objectSchema(map[string]any{
			"paths":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional files or directories to limit the diff to."},
			"staged":            map[string]any{"type": "boolean", "default": false, "description": "Show only staged changes (index vs HEAD)."},
			"include_untracked": map[string]any{"type": "boolean", "default": true, "description": "Include untracked files as additions (ignored when staged=true)."},
		}),
		Fn: func(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
			return gitDiff(ctx, sb, args), nil
		},
	}
}

func gitDiff(ctx context.Context, sb backend.SandboxBackend, args map[string]any) *tool.Result {
	paths, err := projectPaths(args, "paths")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	staged, err := boolArg(args, "staged", false)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	untracked, err := boolArg(args, "include_untracked", true)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	ps := pathspec(paths)
	cached := ""
	if staged {
		cached = " --cached"
	}
	// Before the first commit there is no HEAD; diff against the empty tree.
	base := `base=$(git rev-parse -q --verify HEAD || git hash-object -t tree /dev/null) || exit 1; `
	diff := `git -c core.quotepath=off diff` + cached + ` "$base" --relative -M --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/`
	cmd := gitEnv + base + diff + " --numstat -z" + ps + " && printf '\\n%s\\n' " + gitSplit + " && " + diff + ps
	res := run(ctx, sb, cmd, gitTimeout)
	if res.ExitCode != 0 {
		return errorResult("Error: git diff failed: %s", res.errText())
	}
	stats, patch, ok := strings.Cut(res.Stdout, "\n"+gitSplit+"\n")
	if !ok {
		return errorResult("Error: unexpected git diff output (output may have been truncated)")
	}
	out := GitDiffResult{Files: mergeDiff(parseNumstat(stats), splitUnifiedDiff(patch)), Truncated: res.Truncated}
	if untracked && !staged {
		files, truncated, errRes := untrackedDiffs(ctx, sb, ps)
		if errRes != nil {
			return errRes
		}
		out.Files = append(out.Files, files...)
		out.Truncated = out.Truncated || truncated
	}
	if len(out.Files) > maxDiffFiles {
		out.Files, out.Truncated = out.Files[:maxDiffFiles], true
	}
	if budgetDiffs(out.Files) {
		out.Truncated = true
	}
	return jsonResult(out)
}

// mergeDiff pairs numstat counts with diff text; both come from the same
// diff machinery, so they share paths and order.
func mergeDiff(stats []numstat, chunks []diffChunk) []FileDiff {
	byPath := make(map[string]diffChunk, len(chunks))
	for _, c := range chunks {
		byPath[c.Path] = c
	}
	files := make([]FileDiff, 0, len(stats))
	for i, s := range stats {
		c, ok := byPath[s.Path]
		if !ok && len(chunks) == len(stats) {
			c, ok = chunks[i], true
		}
		f := FileDiff{Path: s.Path, OldPath: s.OldPath, Status: "modified", Additions: s.Additions, Deletions: s.Deletions, Binary: s.Binary}
		if ok {
			f.Status, f.Diff = c.Status, c.Text
			f.Binary = f.Binary || c.Binary
		}
		if f.OldPath != "" && f.Status == "modified" {
			f.Status = "renamed"
		}
		if f.Binary {
			f.Diff = ""
		}
		files = append(files, f)
	}
	return files
}

// untrackedDiffs lists untracked files and diffs the first few against /dev/null.
func untrackedDiffs(ctx context.Context, sb backend.SandboxBackend, ps string) ([]FileDiff, bool, *tool.Result) {
	res := run(ctx, sb, gitEnv+"git ls-files --others --exclude-standard -z"+ps, gitTimeout)
	if res.ExitCode != 0 {
		return nil, false, errorResult("Error: git ls-files failed: %s", res.errText())
	}
	var names []string
	for _, n := range strings.Split(res.Stdout, "\x00") {
		if n = strings.TrimLeft(n, "\n"); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, res.Truncated, nil
	}
	truncated := res.Truncated
	if len(names) > maxUntrackedDiffs {
		names, truncated = names[:maxUntrackedDiffs], true
	}
	var cmd strings.Builder
	cmd.WriteString(gitEnv)
	for _, n := range names {
		fmt.Fprintf(&cmd, "git -c core.quotepath=off diff --no-index --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/ -- /dev/null %s; ", shellQuote(n))
	}
	cmd.WriteString("true")
	diffs := run(ctx, sb, cmd.String(), gitTimeout)
	chunks := map[string]diffChunk{}
	for _, c := range splitUnifiedDiff(diffs.Stdout) {
		chunks[c.Path] = c
	}
	files := make([]FileDiff, 0, len(names))
	for _, n := range names {
		f := FileDiff{Path: n, Status: "untracked"}
		if c, ok := chunks[n]; ok {
			f.Binary = c.Binary
			if !c.Binary {
				f.Diff = c.Text
				f.Additions, f.Deletions = countDiffLines(c.Text)
			}
		}
		files = append(files, f)
	}
	return files, truncated || diffs.Truncated, nil
}

// budgetDiffs clips each diff and drops diffs once the total budget is
// spent. It reports whether anything was cut.
func budgetDiffs(files []FileDiff) bool {
	cut := false
	used := 0
	for i := range files {
		f := &files[i]
		if f.Diff == "" {
			continue
		}
		if used >= maxTotalDiffBytes {
			f.Diff, f.DiffOmitted, cut = "", true, true
			continue
		}
		limit := min(maxFileDiffBytes, maxTotalDiffBytes-used)
		if len(f.Diff) > limit {
			f.Diff = clipText(f.Diff, limit)
			cut = true
		}
		used += len(f.Diff)
	}
	return cut
}

// clipText cuts s at a line boundary under limit bytes and notes the rest.
func clipText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	head := s[:limit]
	if i := strings.LastIndexByte(head, '\n'); i > 0 {
		head = head[:i]
	}
	return head + fmt.Sprintf("\n... (%d more bytes clipped)", len(s)-len(head))
}
