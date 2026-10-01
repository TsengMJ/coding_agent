package codetools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

const applyPatchDescription = `Apply a multi-line or multi-file change in one call. Use edit_file for a small, exact replacement in one place; use apply_patch when a change spans several hunks or files, or adds, deletes or renames files.

The patch format (no line numbers; hunks are located by their content):

*** Begin Patch
*** Update File: /src/app.go
@@ func main() {
 	cfg := load()
-	run(cfg)
+	if err := run(cfg); err != nil {
+		log.Fatal(err)
+	}
*** Add File: /src/new.go
+package src
*** Delete File: /src/old.go
*** Update File: /src/a.go
*** Move to: /src/b.go
@@
-old line
+new line
*** End Patch

Rules:
- Paths use the same virtual absolute paths as the file tools ("/" is the project root).
- In a hunk, lines start with ' ' (unchanged context), '-' (removed) or '+' (added). Include about 3 lines of context around each change.
- "@@ <line>" optionally names a line (e.g. a function signature) that appears before the hunk; use it when the context lines alone are not unique.
- "*** End of File" after a hunk anchors it to the end of the file.
- Each file may appear only once. Added files must not exist yet; updated and deleted files must exist.
- The patch is all-or-nothing: if any hunk does not match, no file is changed and the error shows the lines that were not found. Re-read the file and retry.`

// PatchedFile is the planned outcome of one patch operation.
type PatchedFile struct {
	Op   OpKind
	Path string
	// MoveTo is the new path of a renamed file.
	MoveTo string
	Before string
	After  string
}

// Target is where the file ends up.
func (f PatchedFile) Target() string {
	if f.MoveTo != "" {
		return f.MoveTo
	}
	return f.Path
}

// ReadFunc reads a virtual path; found is false when the file does not exist.
type ReadFunc func(path string) (content string, found bool, err error)

// PlanPatch parses text and computes every file's new content without
// writing anything. It fails if any operation cannot be applied.
func PlanPatch(text string, read ReadFunc) ([]PatchedFile, error) {
	ops, err := ParsePatch(text)
	if err != nil {
		return nil, err
	}
	planned := make([]PatchedFile, 0, len(ops))
	for _, op := range ops {
		f, err := planOp(op, read)
		if err != nil {
			return nil, err
		}
		planned = append(planned, f)
	}
	return planned, nil
}

func planOp(op FileOp, read ReadFunc) (PatchedFile, error) {
	p, err := backend.ValidatePath(op.Path, nil)
	if err != nil {
		return PatchedFile{}, err
	}
	f := PatchedFile{Op: op.Kind, Path: p}
	if op.MoveTo != "" {
		if f.MoveTo, err = backend.ValidatePath(op.MoveTo, nil); err != nil {
			return f, err
		}
		if f.MoveTo == f.Path {
			f.MoveTo = ""
		}
	}
	content, found, err := read(p)
	if err != nil {
		return f, fmt.Errorf("reading %s: %w", p, err)
	}
	switch op.Kind {
	case OpAdd:
		if found {
			return f, fmt.Errorf("cannot add %s: file already exists; use '*** Update File:' to change it", p)
		}
		f.After = op.Content
		return f, nil
	case OpDelete:
		if !found {
			return f, fmt.Errorf("cannot delete %s: file not found", p)
		}
		f.Before = content
		return f, nil
	}
	if !found {
		return f, fmt.Errorf("cannot update %s: file not found; use '*** Add File:' to create it", p)
	}
	if !utf8.ValidString(content) {
		return f, fmt.Errorf("cannot update %s: not a UTF-8 text file", p)
	}
	f.Before = content
	if f.After, err = applyChunks(content, op.Chunks); err != nil {
		return f, fmt.Errorf("%s: %w", p, err)
	}
	if f.MoveTo != "" {
		if _, exists, err := read(f.MoveTo); err != nil {
			return f, fmt.Errorf("reading %s: %w", f.MoveTo, err)
		} else if exists {
			return f, fmt.Errorf("cannot move %s to %s: destination already exists", p, f.MoveTo)
		}
	}
	return f, nil
}

// UnifiedDiff renders the planned change as a unified diff.
func (f PatchedFile) UnifiedDiff() string {
	switch f.Op {
	case OpAdd:
		return udiff.Unified("/dev/null", f.Path, "", f.After)
	case OpDelete:
		return udiff.Unified(f.Path, "/dev/null", f.Before, "")
	}
	return udiff.Unified(f.Path, f.Target(), f.Before, f.After)
}

// PatchResult is the apply_patch result.
type PatchResult struct {
	Files []PatchFileResult `json:"files"`
}

// PatchFileResult summarizes one changed file.
type PatchFileResult struct {
	Path      string `json:"path"`
	MovedFrom string `json:"moved_from,omitempty"`
	Op        OpKind `json:"op"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// NewApplyPatch returns the apply_patch tool.
func NewApplyPatch(b backend.Backend) tool.Tool {
	return &tool.Func{
		ToolName:        ApplyPatchName,
		ToolDescription: applyPatchDescription,
		ToolSchema: objectSchema(map[string]any{
			"patch": map[string]any{"type": "string", "description": "The full patch text, from '*** Begin Patch' to '*** End Patch'."},
		}, "patch"),
		Fn: func(ctx context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
			rc := backend.NewRunContext(nil)
			if rt != nil {
				rc.State, rc.Store, rc.Config = rt.State, rt.Store, rt.Config
			}
			res := applyPatch(backend.WithRunContext(ctx, rc), b, rt, args)
			if files := rc.FilesUpdate(); files != nil {
				res.Update = &runtime.Update{Values: map[string]any{backend.FilesStateKey: files}}
			}
			return res, nil
		},
	}
}

func applyPatch(ctx context.Context, b backend.Backend, rt *tool.Runtime, args map[string]any) *tool.Result {
	text, err := stringArg(args, "patch")
	if err != nil || strings.TrimSpace(text) == "" {
		return errorResult("Error: patch is required and must be a string")
	}
	planned, err := PlanPatch(text, backendReader(ctx, b))
	if err != nil {
		return errorResult("Error: patch not applied, no files were changed.\n%v", err)
	}
	if path, ok := parallelPatchConflict(rt, planned); ok {
		return errorResult("Error: parallel file mutations to the same path are not allowed (%s).", path)
	}
	deleter, canDelete := b.(backend.Deleter)
	for _, f := range planned {
		if (f.Op == OpDelete || f.MoveTo != "") && !canDelete {
			return errorResult("Error: patch not applied: this backend cannot delete files (needed for %s)", f.Path)
		}
	}
	if err := writePlanned(ctx, b, deleter, planned); err != nil {
		return errorResult("Error: %v", err)
	}
	out := PatchResult{Files: make([]PatchFileResult, 0, len(planned))}
	for _, f := range planned {
		r := PatchFileResult{Path: f.Target(), Op: f.Op}
		if f.MoveTo != "" {
			r.MovedFrom = f.Path
		}
		r.Additions, r.Deletions = countDiffLines(f.UnifiedDiff())
		out.Files = append(out.Files, r)
	}
	return jsonResult(out)
}

// backendReader reads files through b for PlanPatch.
func backendReader(ctx context.Context, b backend.Backend) ReadFunc {
	return func(p string) (string, bool, error) {
		resp := b.DownloadFiles(ctx, []string{p})
		if len(resp) != 1 {
			return "", false, errors.New("backend returned no result")
		}
		switch resp[0].Error {
		case "":
			return string(resp[0].Content), true, nil
		case backend.ErrFileNotFound:
			return "", false, nil
		default:
			return "", false, errors.New(resp[0].Error)
		}
	}
}

// writePlanned writes every planned file. When a write fails, files already
// written are restored so the patch stays all-or-nothing.
func writePlanned(ctx context.Context, b backend.Backend, d backend.Deleter, planned []PatchedFile) error {
	var undo []func()
	rollback := func(cause error) error {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
		return fmt.Errorf("patch not applied, changes were rolled back: %w", cause)
	}
	for _, f := range planned {
		switch f.Op {
		case OpAdd:
			if r := b.Write(ctx, f.Path, f.After); r.Error != "" {
				return rollback(errors.New(r.Error))
			}
			if d != nil {
				undo = append(undo, func() { d.Delete(ctx, f.Path) })
			}
		case OpDelete:
			if r := d.Delete(ctx, f.Path); r.Error != "" {
				return rollback(errors.New(r.Error))
			}
			undo = append(undo, func() { b.Write(ctx, f.Path, f.Before) })
		case OpUpdate:
			if r := b.Write(ctx, f.Target(), f.After); r.Error != "" {
				return rollback(errors.New(r.Error))
			}
			if f.MoveTo == "" {
				undo = append(undo, func() { b.Write(ctx, f.Path, f.Before) })
				continue
			}
			undo = append(undo, func() { d.Delete(ctx, f.MoveTo) })
			if r := d.Delete(ctx, f.Path); r.Error != "" {
				return rollback(errors.New(r.Error))
			}
			undo = append(undo, func() { b.Write(ctx, f.Path, f.Before) })
		}
	}
	return nil
}

var mutationTools = []string{"write_file", "edit_file", "delete"}

// parallelPatchConflict reports a path this patch touches that an earlier
// tool call in the same assistant message also mutates. Tool calls run
// concurrently, so only the first mutation of a path may run.
func parallelPatchConflict(rt *tool.Runtime, planned []PatchedFile) (string, bool) {
	if rt == nil || rt.State == nil || rt.ToolCallID == "" {
		return "", false
	}
	mine := map[string]bool{}
	for _, f := range planned {
		mine[f.Path] = true
		mine[f.Target()] = true
	}
	for i := len(rt.State.Messages) - 1; i >= 0; i-- {
		msg := rt.State.Messages[i]
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == rt.ToolCallID {
				return "", false
			}
			for _, p := range mutatedPaths(call) {
				if mine[p] {
					return p, true
				}
			}
		}
		return "", false
	}
	return "", false
}

// mutatedPaths lists the files a tool call changes.
func mutatedPaths(call llm.ToolCall) []string {
	if slices.Contains(mutationTools, call.Name) {
		raw, _ := call.Args["file_path"].(string)
		if p, err := backend.ValidatePath(raw, nil); err == nil && raw != "" {
			return []string{p}
		}
		return nil
	}
	if call.Name != ApplyPatchName {
		return nil
	}
	text, _ := call.Args["patch"].(string)
	ops, err := ParsePatch(text)
	if err != nil {
		return nil
	}
	var out []string
	for _, op := range ops {
		for _, raw := range []string{op.Path, op.MoveTo} {
			if p, err := backend.ValidatePath(raw, nil); err == nil && raw != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
