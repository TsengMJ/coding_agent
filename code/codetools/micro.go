package codetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

// missingMarker is printed instead of a summary when the interpreter for a
// syntax check is not installed.
const missingMarker = "__QUILL_MISSING__"

// SyntaxCheck syntax-checks the Python and JavaScript files among the given
// project-relative paths with the interpreters in sb, without writing
// bytecode. Other files, and files whose interpreter is not installed, are
// skipped. It returns only errors.
func SyntaxCheck(ctx context.Context, sb backend.SandboxBackend, files []string, timeout time.Duration) ([]Diagnostic, error) {
	var py, js []string
	for _, f := range files {
		switch path.Ext(f) {
		case ".py":
			py = append(py, f)
		case ".js", ".cjs", ".mjs":
			js = append(js, f)
		}
	}
	var out []Diagnostic
	for _, c := range []struct {
		files       []string
		interpreter string
		script      string
	}{{py, "python3", pyCompileScript}, {js, "node", nodeCheckScript}} {
		if len(c.files) == 0 {
			continue
		}
		cmd := "command -v " + c.interpreter + " >/dev/null 2>&1 || { echo " + missingMarker + "; echo " + exitMarker + "0; exit 0; }; " +
			c.interpreter + " " + map[string]string{"python3": "-c", "node": "-e"}[c.interpreter] + " " + shellQuote(c.script) + " " + quoteAll(c.files) + "; echo " + exitMarker + "$?"
		res := run(ctx, sb, cmd, timeout)
		outs, codes := splitExit(res.Stdout)
		if len(codes) == 0 {
			return nil, fmt.Errorf("%s syntax check did not finish: %s", c.interpreter, clipRaw(res.errText()))
		}
		if strings.TrimSpace(outs[0]) == missingMarker {
			continue
		}
		s, _, err := parseSummary(res.Stdout)
		if err != nil {
			return nil, fmt.Errorf("%s syntax check: %w", c.interpreter, err)
		}
		for _, d := range s.Diagnostics {
			if d.Severity == "error" {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

// DiagnoseFiles builds and vets the Go packages of the given
// project-relative files, or runs the Node / Python checks on them, and
// returns the diagnostics located in those files. A project it cannot
// check, or a check that does not finish within timeout, is an error.
func DiagnoseFiles(ctx context.Context, sb backend.SandboxBackend, files []string, timeout time.Duration) ([]Diagnostic, error) {
	if len(files) == 0 {
		return nil, nil
	}
	t, res := resolveTarget(ctx, sb, map[string]any{"files": toAny(files)})
	if res != nil {
		return nil, resultError(res)
	}
	tsb := t.sandbox(sb)
	switch t.lang {
	case langGo:
		var pkgs []any
		for _, f := range t.files {
			if p := "./" + path.Dir(f); !slices.Contains(pkgs, any(p)) {
				pkgs = append(pkgs, p)
			}
		}
		secs := max(int(timeout/time.Second), 1)
		res = goDiagnostics(ctx, tsb, t, map[string]any{"packages": pkgs, "timeout_seconds": min(secs, maxGoTimeout)})
	case langNode:
		res = nodeDiagnostics(ctx, tsb, t, timeout)
	case langPython:
		res = pyDiagnostics(ctx, tsb, t, timeout)
	default:
		return nil, fmt.Errorf("no checker for %s", strings.Join(files, ", "))
	}
	if res.IsError {
		return nil, resultError(res)
	}
	var diags []Diagnostic
	if err := json.Unmarshal([]byte(resultText(res)), &diags); err != nil {
		return nil, resultError(res)
	}
	var out []Diagnostic
	for _, d := range diags {
		if slices.Contains(files, d.File) {
			out = append(out, d)
		}
	}
	return out, nil
}

func resultText(res *tool.Result) string {
	var b strings.Builder
	for _, c := range res.Content {
		if c.Type == llm.BlockText {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

func resultError(res *tool.Result) error {
	return errors.New(clipRaw(resultText(res)))
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
