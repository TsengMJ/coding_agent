package client

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/tsengmingchun/quill/apps/agent/code/codetools"
	"github.com/tsengmingchun/quill/apps/agent/middleware/hitl"
)

// diffMaxLines caps how many diff lines an approval preview shows.
const diffMaxLines = 100

// ActionDiff returns a unified diff of what write_file or edit_file would
// change under cwd, or "" for other tools. edit_file is diffed against the
// file on disk; if the file is gone or no longer contains old_string, only
// old_string and new_string are compared.
func ActionDiff(req hitl.ActionRequest, cwd string) string {
	str := func(k string) string { s, _ := req.Args[k].(string); return s }
	path := str("file_path")
	switch req.Name {
	case "write_file":
		before, err := readUnder(cwd, path)
		if err != nil {
			return clipDiff(udiff.Unified("/dev/null", path, "", withNewline(str("content"))))
		}
		return clipDiff(udiff.Unified(path, path, before, str("content")))
	case "edit_file":
		old, repl := str("old_string"), str("new_string")
		before, err := readUnder(cwd, path)
		if err != nil || old == "" || !strings.Contains(before, old) {
			return clipDiff(udiff.Unified(path, path, withNewline(old), withNewline(repl)))
		}
		n := 1
		if all, _ := req.Args["replace_all"].(bool); all {
			n = -1
		}
		return clipDiff(udiff.Unified(path, path, before, strings.Replace(before, old, repl, n)))
	case "apply_patch":
		return patchDiff(str("patch"), cwd)
	default:
		return ""
	}
}

// patchDiff previews an apply_patch call as one unified diff per file; ""
// (falling back to the raw patch) when the patch would not apply.
func patchDiff(patch, cwd string) string {
	planned, err := codetools.PlanPatch(patch, func(p string) (string, bool, error) {
		s, err := readUnder(cwd, p)
		switch {
		case err == nil:
			return s, true, nil
		case errors.Is(err, os.ErrNotExist):
			return "", false, nil
		default:
			return "", false, err
		}
	})
	if err != nil {
		return ""
	}
	parts := make([]string, len(planned))
	for i, f := range planned {
		parts[i] = strings.TrimRight(f.UnifiedDiff(), "\n")
	}
	return clipDiff(strings.Join(parts, "\n"))
}

// ActionPreviewIn prefers a diff against the files in cwd and falls back to
// ActionPreview.
func ActionPreviewIn(req hitl.ActionRequest, cwd string) string {
	if cwd != "" {
		if d := ActionDiff(req, cwd); d != "" {
			return d
		}
	}
	return ActionPreview(req)
}

// readUnder reads a virtual path ("/src/a.go") relative to cwd, refusing
// paths that would leave it.
func readUnder(cwd, path string) (string, error) {
	if cwd == "" || path == "" {
		return "", os.ErrNotExist
	}
	full := filepath.Join(cwd, filepath.FromSlash(path))
	if rel, err := filepath.Rel(cwd, full); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	raw, err := os.ReadFile(full)
	return string(raw), err
}

func withNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

func clipDiff(d string) string {
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	if len(lines) <= diffMaxLines {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:diffMaxLines], "\n") + fmt.Sprintf("\n... (%d more diff lines)", len(lines)-diffMaxLines)
}
