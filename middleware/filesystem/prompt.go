package filesystem

// Default tool descriptions (Python `middleware/filesystem.py`).
const (
	lsDescription = `Lists all files in a directory.

This is useful for exploring the filesystem and finding the right file to read or edit.
You should almost ALWAYS use this tool before using the read_file or edit_file tools.`

	readFileDescription = "Reads a file from the filesystem. Assume any path the user provides is valid; reading a missing file returns an error.\n\n" +
		"Usage:\n" +
		"- By default, it reads up to 100 lines starting from the beginning of the file. Use `offset`/`limit` to page through large files instead of reading them whole.\n" +
		"- A status header, `@@ field | field | ... @@`, sits above the file content, and every line after it is verbatim file content. When content is truncated, there may be an explanation before the header. Never include the header when editing.\n" +
		"- Speculatively batch multiple `read_file` calls in one response when several files may be useful.\n" +
		"- An empty file returns a system-reminder warning in place of contents.\n" +
		"- Images (`.png`, `.jpg`, etc.), audio, video, and PDFs return multimodal content blocks.\n" +
		"- For images and PDFs, pagination via `offset`/`limit` is text-only - supply `file_path` only\n" +
		"- Always read a file before editing it."

	editFileDescription = `Performs exact string replacements in files.

Usage:
- You must read the file before editing; this tool errors otherwise.
- Preserve the exact source indentation from the read output, and never include the read status header in old_string or new_string.
- Prefer editing an existing file over creating a new one.
- Only use emojis if the user explicitly requests it.`

	writeFileDescription = `Writes content to a file. Creates the file if it does not exist; replaces it entirely if it does.

Usage:
- Use this tool when you intend to create a new file or replace the whole file. You do not need to read the file first.
- Prefer to edit existing files (with the edit_file tool) over creating new ones when possible.
`

	deleteDescription = `Deletes a file or directory from the filesystem.

Usage:
- Permanently removes the file or directory at the given absolute path.
- Deleting a directory removes it and everything inside it, recursively. Prefer
  deleting a directory in one call over deleting each file individually.
- This cannot be undone, so only delete paths you are sure are no longer needed.
`

	globDescription = "Find files matching a glob pattern, returning absolute paths.\n\n" +
		"Supports `*` (any characters within a path segment), `**` (any directories), `?` (single character), `[abc]` (one character from a set), and `{a,b}` (alternatives), e.g. `*.py`, `src/**/*.py`, `*.{yml,yaml}`.\n\n" +
		"A pattern without `/` matches the file name at any depth under the search root (`*.py` matches `src/app/main.py`). A pattern containing `/` matches the search-root-relative path (`src/**/*.py`). A leading `/` anchors to the search root (`/*.py` matches only top-level Python files)."

	grepDescription = "Search for a LITERAL text pattern across files (NOT regex).\n\n" +
		"The pattern is matched verbatim: regex metacharacters are ordinary characters, not operators. To match any of several strings, run a separate grep for each; `grep(pattern=\"foo|bar\")` searches for the literal text \"foo|bar\", and `.*` or `\\.` match those characters literally."

	grepExecuteFallback = "\n- If you genuinely need regex, use the execute tool with `rg '<regex>'` instead."

	executeDescription = `Executes a shell command and returns combined stdout/stderr with the exit code (truncated if very large).

Usage:
- Quote paths containing spaces (e.g. cd "/path/with spaces").
- Chain commands with ';' or '&&' (use '&&' when a command depends on the previous); do not use newlines except inside quoted strings.
- Commands run in the project root directory; avoid ` + "`cd`" + ` so the working directory stays stable; use the optional timeout to override the default.
- You MUST avoid using search commands like find and grep. Instead use the grep, glob tools to search. Use read_file rather than cat/head/tail.
    - execute(command="find . -name '*.py'")  # Use glob tool instead
    - execute(command="grep -r 'pattern' .")  # Use grep tool instead`
)

// Parameter descriptions shared by the tool schemas.
const (
	grepGlobParamDescription = "Glob pattern (NOT regex) limiting which files are searched (e.g. '*.py', " +
		"'*.ts'). A pattern without '/' matches the file name at any depth; a pattern " +
		"containing '/' matches the search-root-relative path (e.g. 'src/**/*.py'). " +
		"This is an in-tool file filter, not a call to the separate glob tool."

	grepOutputModeParamDescription = "Shape of the returned text. 'files_with_matches' (default): newline-separated " +
		"matching file paths. 'content': matching lines grouped by file under a " +
		"'<path>:' header, each line indented and formatted '<line_number>: <line text>' " +
		"(only the matched line, no surrounding context). 'count': one " +
		"'<path>: <match_count>' line per file."
)

// Model-facing notes appended to tool results.
const (
	noLinesRequestedWarning = "System reminder: no lines were read because `limit` was %d. The file was " +
		"not inspected and may have contents; retry with `limit` >= 1 to read it."

	grepTruncationNote = "Note: the search stopped early (it hit its time limit or the maximum match count). " +
		"The matches above are valid but incomplete. Narrow the search (a more specific pattern or a " +
		"narrower path), or raise max_count, to see the rest."

	globTruncationNote = "Note: the search stopped early because it hit its time or size limit. The paths above are " +
		"valid but incomplete. Narrow the search (a more specific pattern or a narrower path) to see " +
		"the rest."

	executeUnavailable = "Error: Execution not available. This agent's backend " +
		"does not support command execution (SandboxBackendProtocol). " +
		"To use the execute tool, provide a backend that implements SandboxBackendProtocol."
)
