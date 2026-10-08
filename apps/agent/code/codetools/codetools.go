// Package codetools holds quill-only coding tools that turn noisy command
// output into structured JSON for the model: git_status, git_diff,
// apply_patch, run_tests and diagnostics. Commands run through the backend's Execute, so the tools work
// wherever `execute` does (host shell or the quillbench container).
package codetools

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// Tool names.
const (
	GitStatusName   = "git_status"
	GitDiffName     = "git_diff"
	ApplyPatchName  = "apply_patch"
	RunTestsName    = "run_tests"
	DiagnosticsName = "diagnostics"
)

// New returns the coding tools b supports: apply_patch always, the command
// based tools only when b can execute commands.
func New(b backend.Backend) []tool.Tool {
	tools := []tool.Tool{NewApplyPatch(b)}
	if sb, ok := b.(backend.SandboxBackend); ok {
		tools = append(tools, NewGitStatus(sb), NewGitDiff(sb), NewRunTests(sb), NewDiagnostics(sb))
	}
	return tools
}

// jsonResult renders v as compact JSON and attaches it as the artifact.
func jsonResult(v any) *tool.Result {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return errorResult("Error: encoding result: %v", err)
	}
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(string(bytes.TrimRight(buf.Bytes(), "\n")))}, Artifact: v}
}

func errorResult(format string, a ...any) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf(format, a...))}, IsError: true}
}

func objectSchema(props map[string]any, required ...string) tool.Schema {
	if required == nil {
		required = []string{}
	}
	return tool.Schema{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func boolArg(args map[string]any, key string, def bool) (bool, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}

func stringArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func intArg(args map[string]any, key string, def int) (int, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(n), nil
	case int:
		return n, nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		return int(i), nil
	}
	return 0, fmt.Errorf("%s must be an integer", key)
}

// stringsArg accepts a list of strings or a single string.
func stringsArg(args map[string]any, key string) ([]string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return nil, nil
	}
	switch x := v.(type) {
	case string:
		return []string{x}, nil
	case []string:
		return x, nil
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings", key)
			}
			out[i] = s
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a list of strings", key)
}

// projectPaths validates a list of path arguments.
func projectPaths(args map[string]any, key string) ([]string, error) {
	raw, err := stringsArg(args, key)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		p, err := projectPath(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
