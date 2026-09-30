package filesystem

import (
	"context"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

const (
	defaultReadOffset = 0
	defaultReadLimit  = 100
)

func textResult(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}}
}

func errorResult(format string, a ...any) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(fmt.Sprintf(format, a...))}, IsError: true}
}

func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func objectSchema(props map[string]any, required ...string) tool.Schema {
	return tool.Schema{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func (m *Middleware) description(name ToolName, def string) string {
	if d := m.opts.CustomToolDescriptions[string(name)]; d != "" {
		return d
	}
	return def
}

func (m *Middleware) newTool(name ToolName, def string, schema tool.Schema, fn func(context.Context, args) *tool.Result) tool.Tool {
	return &tool.Func{
		ToolName:        string(name),
		ToolDescription: m.description(name, def),
		ToolSchema:      schema,
		Fn: func(ctx context.Context, rt *tool.Runtime, raw map[string]any) (*tool.Result, error) {
			rc := backend.NewRunContext(nil)
			if rt != nil {
				rc.State, rc.Store, rc.Config = rt.State, rt.Store, rt.Config
			}
			res := fn(backend.WithRunContext(ctx, rc), args(raw))
			if files := rc.FilesUpdate(); files != nil {
				res.Update = &runtime.Update{Values: map[string]any{backend.FilesStateKey: files}}
			}
			return res, nil
		},
	}
}

func (m *Middleware) buildTool(name ToolName) tool.Tool {
	switch name {
	case ToolLs:
		return m.newTool(name, lsDescription, objectSchema(map[string]any{
			"path": stringProp("Absolute path to the directory to list. Must be absolute, not relative."),
		}, "path"), m.ls)
	case ToolReadFile:
		return m.newTool(name, readFileDescription, objectSchema(map[string]any{
			"file_path": stringProp("Absolute path to the file to read. Must be absolute, not relative."),
			"offset":    map[string]any{"type": "integer", "default": defaultReadOffset, "description": "Line number to start reading from (0-indexed). Use for pagination of large files."},
			"limit":     map[string]any{"type": "integer", "default": defaultReadLimit, "description": "Maximum number of lines to read. Use for pagination of large files."},
		}, "file_path"), m.readFile)
	case ToolWriteFile:
		return m.newTool(name, writeFileDescription, objectSchema(map[string]any{
			"file_path": stringProp("Absolute path where the file should be written. Must be absolute, not relative."),
			"content":   stringProp("The text content to write to the file. This parameter is required."),
		}, "file_path", "content"), m.writeFile)
	case ToolEditFile:
		return m.newTool(name, editFileDescription, objectSchema(map[string]any{
			"file_path":   stringProp("Absolute path to the file to edit. Must be absolute, not relative."),
			"old_string":  stringProp("The exact text to find and replace. Must be unique in the file unless replace_all is True."),
			"new_string":  stringProp("The text to replace old_string with. Must be different from old_string."),
			"replace_all": map[string]any{"type": "boolean", "default": false, "description": "If True, replace all occurrences of old_string. If False (default), old_string must be unique."},
		}, "file_path", "old_string", "new_string"), m.editFile)
	case ToolDelete:
		return m.newTool(name, deleteDescription, objectSchema(map[string]any{
			"file_path": stringProp("Absolute path to the file to delete. Must be absolute, not relative."),
		}, "file_path"), m.delete)
	case ToolGlob:
		return m.newTool(name, globDescription, objectSchema(map[string]any{
			"pattern": stringProp("Glob pattern to match files (e.g., '*.py', '**/*.py', '/subdir/**/*.md'). " +
				"A pattern without '/' matches the file name at any depth; a pattern containing '/' matches the search-root-relative path."),
			"path": stringProp("Base directory to search from. Defaults to the backend's default root."),
		}, "pattern"), m.glob)
	case ToolGrep:
		desc := grepDescription
		if backend.SupportsExecution(m.opts.Backend) {
			desc += grepExecuteFallback
		}
		return m.newTool(name, desc, objectSchema(map[string]any{
			"pattern":     stringProp("Text pattern to search for (literal string, not regex)."),
			"path":        stringProp("Directory to search in. Defaults to current working directory."),
			"glob":        stringProp(grepGlobParamDescription),
			"output_mode": map[string]any{"type": "string", "enum": []string{"files_with_matches", "content", "count"}, "default": "files_with_matches", "description": grepOutputModeParamDescription},
			"max_count": map[string]any{"type": "integer", "exclusiveMinimum": 0, "description": "Optional cap on the total number of matches returned across all files. " +
				"Leave unset to use the configured default. When the cap is hit, results are truncated and a note says so; narrow the pattern or path to see the rest."},
		}, "pattern"), m.grep)
	case ToolExecute:
		return m.newTool(name, executeDescription, objectSchema(map[string]any{
			"command": stringProp("Shell command to execute."),
			"timeout": map[string]any{"type": "integer", "description": "Optional timeout in seconds for this command. Overrides the default timeout."},
		}, "command"), m.execute)
	}
	panic("filesystem: unknown tool " + string(name))
}

// validPath validates a model-supplied path argument.
func validPath(a args, key string) (string, *tool.Result) {
	raw, err := a.requiredString(key)
	if err != nil {
		return "", errorResult("Error: %v", err)
	}
	p, err := backend.ValidatePath(raw, nil)
	if err != nil {
		return "", errorResult("Error: %v", err)
	}
	return p, nil
}

func (m *Middleware) ls(ctx context.Context, a args) *tool.Result {
	p, errRes := validPath(a, "path")
	if errRes != nil {
		return errRes
	}
	res := m.opts.Backend.Ls(ctx, p)
	if res.Error != "" {
		return errorResult("Error: %s", res.Error)
	}
	paths := make([]string, len(res.Entries))
	for i, e := range res.Entries {
		paths[i] = e.Path
	}
	return textResult(formatPaths(paths))
}

func formatPaths(paths []string) string {
	if len(paths) == 0 {
		return "No files found"
	}
	return backend.TruncateIfTooLong(strings.Join(paths, "\n"))
}

func (m *Middleware) readFile(ctx context.Context, a args) *tool.Result {
	p, errRes := validPath(a, "file_path")
	if errRes != nil {
		return errRes
	}
	offset, err := a.optionalInt("offset", defaultReadOffset)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	limit, err := a.optionalInt("limit", defaultReadLimit)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	res := m.opts.Backend.Read(ctx, p, offset, limit)
	if res.Error != "" {
		return errorResult("Error: %s", res.Error)
	}
	if res.FileData == nil {
		return errorResult("Error: no data returned for '%s'", p)
	}
	content := res.FileData.Content
	wholeFile := res.StartLine == nil || (*res.StartLine == 1 && res.TotalLines != nil && *res.TotalLines == *res.EndLine)
	if msg := backend.CheckEmptyContent(content); msg != "" && wholeFile {
		if content == "" && res.NoLinesRequested {
			msg = fmt.Sprintf(noLinesRequestedWarning, limit)
		}
		return textResult(msg)
	}
	if fileType := backend.FileTypeForPath(p); res.FileData.Encoding == backend.EncodingBase64 || fileType != backend.FileTypeText {
		return mediaResult(p, fileType, content)
	}
	return textResult(assembleRead(content, res, offset))
}

// assembleRead renders "[notice]\n@@ lines A-B of T | next offset N @@\n<verbatim body>".
func assembleRead(content string, res backend.ReadResult, offset int) string {
	body := strings.TrimSuffix(content, "\n")
	start, end := max(offset, 0)+1, 0
	if res.StartLine != nil && res.EndLine != nil {
		start, end = *res.StartLine, *res.EndLine
	} else {
		end = start + strings.Count(body, "\n")
	}
	span := fmt.Sprintf("lines %d-%d", start, end)
	if res.TotalLines != nil {
		span += fmt.Sprintf(" of %d", *res.TotalLines)
	}
	fields := []string{span}
	if res.NextOffset != nil && (res.TotalLines == nil || end < *res.TotalLines) {
		fields = append(fields, fmt.Sprintf("next offset %d", *res.NextOffset))
	}
	var parts []string
	if offset < 0 {
		parts = append(parts, fmt.Sprintf("[Requested offset %d is before the start of the file; read from line 1 instead.]", offset))
	}
	parts = append(parts, "@@ "+strings.Join(fields, " | ")+" @@", body)
	return strings.Join(parts, "\n")
}

func mediaResult(p string, fileType backend.FileType, b64 string) *tool.Result {
	mimeType := mime.TypeByExtension(path.Ext(p))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	blockType := llm.BlockFile
	switch fileType {
	case backend.FileTypeImage:
		blockType = llm.BlockImage
	case backend.FileTypeAudio:
		blockType = llm.BlockAudio
	case backend.FileTypeVideo:
		blockType = llm.BlockVideo
	}
	return &tool.Result{Content: []llm.ContentBlock{{Type: blockType, Base64: b64, MimeType: mimeType}}}
}

func (m *Middleware) writeFile(ctx context.Context, a args) *tool.Result {
	p, errRes := validPath(a, "file_path")
	if errRes != nil {
		return errRes
	}
	content, err := a.requiredString("content")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	res := m.opts.Backend.Write(ctx, p, content)
	if res.Error != "" {
		return errorResult("%s", res.Error)
	}
	return textResult("Updated file " + res.Path)
}

func (m *Middleware) editFile(ctx context.Context, a args) *tool.Result {
	p, errRes := validPath(a, "file_path")
	if errRes != nil {
		return errRes
	}
	oldString, err := a.requiredString("old_string")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	newString, err := a.requiredString("new_string")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	replaceAll, err := a.optionalBool("replace_all", false)
	if err != nil {
		return errorResult("Error: %v", err)
	}
	res := m.opts.Backend.Edit(ctx, p, oldString, newString, replaceAll)
	if res.Error != "" {
		return errorResult("%s", res.Error)
	}
	return textResult(fmt.Sprintf("Successfully replaced %d instance(s) of the string in '%s'", res.Occurrences, res.Path))
}

func (m *Middleware) delete(ctx context.Context, a args) *tool.Result {
	p, errRes := validPath(a, "file_path")
	if errRes != nil {
		return errRes
	}
	d, ok := m.opts.Backend.(backend.Deleter)
	if !ok {
		return errorResult("Error: delete is not supported by this backend")
	}
	res := d.Delete(ctx, p)
	if res.Error != "" {
		return errorResult("%s", res.Error)
	}
	return textResult("Deleted " + res.Path)
}

func (m *Middleware) glob(ctx context.Context, a args) *tool.Result {
	pattern, err := a.requiredString("pattern")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	searchPath, err := a.optionalString("path", "/")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	if searchPath, err = backend.ValidatePath(searchPath, nil); err != nil {
		return errorResult("Error: %v", err)
	}
	res := m.opts.Backend.Glob(ctx, pattern, searchPath)
	if res.Error != "" && len(res.Matches) == 0 {
		return errorResult("Error: %s", res.Error)
	}
	paths := make([]string, len(res.Matches))
	for i, info := range res.Matches {
		paths[i] = info.Path
	}
	out := formatPaths(paths)
	if res.Truncated {
		out += "\n\n" + globTruncationNote
	}
	if res.Error != "" {
		out = "Error: " + res.Error + "\n\nPartial matches:\n" + out
	}
	return textResult(out)
}

func (m *Middleware) grep(ctx context.Context, a args) *tool.Result {
	pattern, err := a.requiredString("pattern")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	searchPath, err := a.optionalString("path", "/")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	if searchPath, err = backend.ValidatePath(searchPath, nil); err != nil {
		return errorResult("Error: %v", err)
	}
	glob, err := a.optionalString("glob", "")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	mode, err := a.optionalString("output_mode", string(backend.GrepFilesWithMatches))
	if err != nil {
		return errorResult("Error: %v", err)
	}
	maxCount, err := a.optionalInt("max_count", m.opts.GrepMaxCount)
	if err != nil || maxCount <= 0 {
		return errorResult("Error: max_count must be a positive integer")
	}
	res := m.opts.Backend.Grep(ctx, pattern, backend.GrepOptions{Path: searchPath, Glob: glob, MaxCount: maxCount})
	if res.Error != "" && len(res.Matches) == 0 {
		return errorResult("%s", res.Error)
	}
	formatted, err := backend.FormatGrepMatches(res.Matches, backend.GrepOutputMode(mode))
	if err != nil {
		return errorResult("Error: %v", err)
	}
	out := backend.TruncateIfTooLong(formatted)
	if res.Error != "" {
		return errorResult("%s\n\nPartial matches:\n%s", res.Error, out)
	}
	var notes []string
	if res.Truncated {
		notes = append(notes, grepTruncationNote)
	}
	if len(res.Matches) == 0 {
		if hint := backend.RegexLiteralHint(pattern); hint != "" {
			notes = append(notes, hint)
		}
	}
	if len(notes) > 0 {
		out += "\n\n" + strings.Join(notes, "\n\n")
	}
	return textResult(out)
}

func (m *Middleware) execute(ctx context.Context, a args) *tool.Result {
	command, err := a.requiredString("command")
	if err != nil {
		return errorResult("Error: %v", err)
	}
	sandbox, ok := m.opts.Backend.(backend.SandboxBackend)
	if !ok {
		return errorResult(executeUnavailable)
	}
	var timeout *time.Duration
	if a["timeout"] != nil {
		secs, err := a.optionalInt("timeout", 0)
		if err != nil {
			return errorResult("Error: %v", err)
		}
		if secs < 0 {
			return errorResult("Error: timeout must be non-negative, got %d.", secs)
		}
		d := time.Duration(secs) * time.Second
		if d > m.opts.MaxExecuteTimeout {
			return errorResult("Error: timeout %ds exceeds maximum allowed (%ds).", secs, int(m.opts.MaxExecuteTimeout.Seconds()))
		}
		timeout = &d
	}
	res := sandbox.Execute(ctx, command, timeout)
	var b strings.Builder
	b.WriteString(res.Output)
	if res.ExitCode != nil {
		status := "succeeded"
		if *res.ExitCode != 0 {
			status = "failed"
		}
		fmt.Fprintf(&b, "\n[Command %s with exit code %d]", status, *res.ExitCode)
	}
	if res.Truncated {
		b.WriteString("\n[Output was truncated due to size limits]")
	}
	out := textResult(b.String())
	out.Artifact = backend.ExecuteArtifact{ExitCode: res.ExitCode}
	return out
}
