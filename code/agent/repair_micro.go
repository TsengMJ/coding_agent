package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/code/codetools"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

const (
	microMarker = "[micro]"
	// defaultMicroTimeout bounds the compile / lint check after an edit;
	// a check that takes longer is skipped.
	defaultMicroTimeout = 20 * time.Second
	syntaxTimeout       = 10 * time.Second
	maxMicroDiagnostics = 10
)

var patchMoveLine = regexp.MustCompile(`(?m)^\*\*\* Move to: (.+)$`)

// checkedExts are the files the compile / lint check runs for.
var checkedExts = map[string]bool{
	".go": true, ".py": true,
	".js": true, ".cjs": true, ".mjs": true, ".jsx": true,
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
}

// MicroCheck checks the files a successful edit changed, right away. An
// edit that introduces a syntax error (the file parsed before it, or is
// new) is rolled back, all files of the call together, and returned as an
// error, as SWE-agent's linting editor does. Errors that were already there
// are only reported. Then it builds / lints the changed files and appends
// their errors as a [micro] block, without rolling back.
type MicroCheck struct {
	// Files is the agent's backend: edits are read and rolled back through
	// it, so the version guard tracks the rollback.
	Files backend.Backend
	// Workspace runs the Python / JavaScript syntax check and the compile /
	// lint check; nil checks Go syntax only.
	Workspace backend.SandboxBackend
	// Timeout bounds the compile / lint check (default 20s).
	Timeout time.Duration
}

func (MicroCheck) Name() string { return "MicroCheckMiddleware" }

// fileState is a file before or after an edit.
type fileState struct {
	content string
	exists  bool
}

type microNote struct {
	Status      string         `json:"status"`
	Note        string         `json:"note,omitempty"`
	Diagnostics []microFinding `json:"diagnostics"`
	Kept        []string       `json:"kept,omitempty"`
}

type microFinding struct {
	File    string `json:"file"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"col,omitempty"`
	Source  string `json:"source,omitempty"`
	Message string `json:"message"`
}

func (m MicroCheck) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	if !mutatingTools[req.ToolCall.Name] || m.Files == nil {
		return next(ctx, req)
	}
	paths := microPaths(req.ToolCall)
	before := m.snapshot(ctx, paths)
	res, err := next(ctx, req)
	if err != nil || res == nil || res.IsError || strings.HasPrefix(resultText(res), "Error") {
		return res, err
	}
	after := m.snapshot(ctx, paths)
	if errs := m.syntaxErrors(ctx, after); len(errs) > 0 {
		return m.onSyntaxError(ctx, res, before, after, errs), nil
	}
	if diags := m.diagnose(ctx, after); len(diags) > 0 {
		return appendNote(res, microNote{Status: "failed", Note: "Fix these errors in the changed files before running tests.", Diagnostics: findings(diags)}), nil
	}
	return res, nil
}

// onSyntaxError rolls the call back when it introduced a syntax error, or
// keeps it and reports errors the files already had.
func (m MicroCheck) onSyntaxError(ctx context.Context, res *tool.Result, before, after map[string]fileState, errs []codetools.Diagnostic) *tool.Result {
	broken := map[string]bool{}
	for _, d := range errs {
		broken[d.File] = true
	}
	introduced := false
	var onDisk []string
	for p := range broken {
		b, ok := before["/"+p]
		switch {
		case !ok || !b.exists:
			introduced = true
		case path.Ext(p) == ".go":
			introduced = introduced || len(goSyntax(p, b.content)) == 0
		default:
			onDisk = append(onDisk, p)
		}
	}
	kept := m.restore(ctx, before, after)
	if !introduced && len(onDisk) > 0 && m.Workspace != nil {
		old, err := codetools.SyntaxCheck(ctx, m.Workspace, onDisk, syntaxTimeout)
		if err == nil {
			hadErrors := map[string]bool{}
			for _, d := range old {
				hadErrors[d.File] = true
			}
			for _, p := range onDisk {
				introduced = introduced || !hadErrors[p]
			}
		}
	}
	note := microNote{Diagnostics: findings(errs)}
	if introduced {
		note.Status, note.Kept = "rolled_back", kept
		msg := "Error: edit rolled back: it introduced a syntax error, so the files are unchanged. Fix the edit and apply it again."
		if len(kept) > 0 {
			msg = "Error: edit rolled back except " + strings.Join(kept, ", ") + ", which could not be restored: it introduced a syntax error. Check those files before retrying."
		}
		return appendNote(&tool.Result{Content: []llm.ContentBlock{llm.TextBlock(msg)}, IsError: true}, note)
	}
	m.restore(ctx, after, before)
	note.Status, note.Note = "syntax_error", "These syntax errors were already in the file before this edit; fix them before running tests."
	return appendNote(res, note)
}

// microPaths lists every path the call may change, including the
// destination of an apply_patch move.
func microPaths(call llm.ToolCall) []string {
	out := editedFiles(call)
	if call.Name == codetools.ApplyPatchName {
		patch, _ := call.Args["patch"].(string)
		for _, m := range patchMoveLine.FindAllStringSubmatch(patch, -1) {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	for i, p := range out {
		out[i] = "/" + strings.TrimPrefix(path.Clean("/"+p), "/")
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (m MicroCheck) snapshot(ctx context.Context, paths []string) map[string]fileState {
	out := map[string]fileState{}
	for i, r := range m.Files.DownloadFiles(ctx, paths) {
		switch r.Error {
		case "":
			out[paths[i]] = fileState{content: string(r.Content), exists: true}
		case backend.ErrFileNotFound:
			out[paths[i]] = fileState{}
		}
	}
	return out
}

// restore puts every file back to want, given that it is now in have, and
// returns the files it could not restore.
func (m MicroCheck) restore(ctx context.Context, want, have map[string]fileState) []string {
	ctx = context.WithoutCancel(ctx)
	deleter, canDelete := m.Files.(backend.Deleter)
	var kept []string
	for p, w := range want {
		h, ok := have[p]
		if !ok || h == w {
			continue
		}
		var msg string
		switch {
		case w.exists:
			msg = m.Files.Write(ctx, p, w.content).Error
		case canDelete:
			msg = deleter.Delete(ctx, p).Error
		default:
			msg = "cannot delete"
		}
		if msg != "" {
			kept = append(kept, strings.TrimPrefix(p, "/"))
		}
	}
	slices.Sort(kept)
	return kept
}

// syntaxErrors parses Go files in process and checks Python / JavaScript
// files through the workspace. A check that cannot run reports nothing.
func (m MicroCheck) syntaxErrors(ctx context.Context, files map[string]fileState) []codetools.Diagnostic {
	var out []codetools.Diagnostic
	var others []string
	for _, p := range sortedKeys(files) {
		f := files[p]
		if !f.exists {
			continue
		}
		rel := strings.TrimPrefix(p, "/")
		if path.Ext(rel) == ".go" {
			out = append(out, goSyntax(rel, f.content)...)
		} else {
			others = append(others, rel)
		}
	}
	if len(others) > 0 && m.Workspace != nil {
		if diags, err := codetools.SyntaxCheck(ctx, m.Workspace, others, syntaxTimeout); err == nil {
			out = append(out, diags...)
		}
	}
	return out
}

// goSyntax parses a Go file and returns its syntax errors.
func goSyntax(name, content string) []codetools.Diagnostic {
	_, err := parser.ParseFile(token.NewFileSet(), name, content, parser.AllErrors|parser.SkipObjectResolution)
	list, ok := err.(scanner.ErrorList)
	if !ok {
		if err != nil {
			return []codetools.Diagnostic{{File: name, Severity: "error", Source: "go/parser", Message: err.Error()}}
		}
		return nil
	}
	var out []codetools.Diagnostic
	for _, e := range list {
		out = append(out, codetools.Diagnostic{File: name, Line: e.Pos.Line, Column: e.Pos.Column, Severity: "error", Source: "go/parser", Message: e.Msg})
	}
	return out
}

// diagnose builds / lints the changed files, one language at a time, and
// returns their errors. Checks that fail to run or time out are skipped.
func (m MicroCheck) diagnose(ctx context.Context, files map[string]fileState) []codetools.Diagnostic {
	if m.Workspace == nil {
		return nil
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = defaultMicroTimeout
	}
	groups := map[string][]string{}
	for _, p := range sortedKeys(files) {
		rel := strings.TrimPrefix(p, "/")
		if files[p].exists && checkedExts[path.Ext(rel)] {
			lang := map[string]string{".go": "go", ".py": "python"}[path.Ext(rel)]
			if lang == "" {
				lang = "node"
			}
			groups[lang] = append(groups[lang], rel)
		}
	}
	var out []codetools.Diagnostic
	for _, lang := range sortedKeys(groups) {
		diags, err := codetools.DiagnoseFiles(ctx, m.Workspace, groups[lang], timeout)
		if err != nil {
			continue
		}
		for _, d := range diags {
			if d.Severity == "error" {
				out = append(out, d)
			}
		}
	}
	return out
}

func findings(diags []codetools.Diagnostic) []microFinding {
	var out []microFinding
	for i, d := range diags {
		if i == maxMicroDiagnostics {
			out = append(out, microFinding{Message: fmt.Sprintf("... and %d more", len(diags)-i)})
			break
		}
		out = append(out, microFinding{File: d.File, Line: d.Line, Column: d.Column, Source: d.Source, Message: clipLine(strings.TrimSpace(d.Message), 300)})
	}
	return out
}

func appendNote(res *tool.Result, note microNote) *tool.Result {
	raw, err := json.Marshal(note)
	if err != nil {
		return res
	}
	out := *res
	out.Content = append(slices.Clone(res.Content), llm.TextBlock("\n\n"+microMarker+" "+string(raw)))
	return &out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
