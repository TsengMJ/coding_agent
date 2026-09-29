package backend

import "github.com/tsengmingchun/deepagents_golang/internal/notimpl"

// Shared constants from backends/utils.py.
const (
	EmptyContentWarning      = "System reminder: File exists but has empty contents"
	EmptyOldStringError      = "Error: old_string cannot be empty. Provide the exact text to replace."
	TruncationMarkerTemplate = "... [%d lines truncated] ..."
	MaxLineLength            = 5000
	ToolResultTokenLimit     = 20000
	TruncationGuidance       = "... [results truncated, try being more specific with your parameters]"
	MaxVideoInputBytes       = 1024 * 1024 * 1024
)

// FileType classifies a path for read_file (text vs. multimodal).
type FileType string

const (
	FileTypeText  FileType = "text"
	FileTypeImage FileType = "image"
	FileTypeAudio FileType = "audio"
	FileTypeVideo FileType = "video"
	FileTypePDF   FileType = "pdf"
	FileTypeFile  FileType = "file"
)

// ValidatePath normalizes a virtual path and rejects traversal or paths outside allowedPrefixes.
func ValidatePath(path string, allowedPrefixes []string) (string, error) {
	return "", notimpl.Err("backend.ValidatePath")
}

// ToPOSIXPath converts OS-specific separators to forward slashes.
func ToPOSIXPath(path string) string { return path }

// FileTypeForPath infers the FileType from the file extension.
func FileTypeForPath(path string) FileType { return FileTypeText }

// FormatContentWithLineNumbers renders cat -n style output starting at startLine.
func FormatContentWithLineNumbers(content string, startLine int) (string, error) {
	return "", notimpl.Err("backend.FormatContentWithLineNumbers")
}

// CheckEmptyContent returns EmptyContentWarning for empty content, else "".
func CheckEmptyContent(content string) string { return "" }

// CreateFileData builds FileData stamped with the current time.
func CreateFileData(content, encoding string) (FileData, error) {
	return FileData{}, notimpl.Err("backend.CreateFileData")
}

// UpdateFileData replaces content and bumps ModifiedAt.
func UpdateFileData(fd FileData, content string) (FileData, error) {
	return FileData{}, notimpl.Err("backend.UpdateFileData")
}

// FileDataToString decodes FileData content to a string.
func FileDataToString(fd FileData) (string, error) {
	return "", notimpl.Err("backend.FileDataToString")
}

// NormalizeReadBounds clamps a negative offset to 0 and reports a non-positive limit.
func NormalizeReadBounds(offset, limit int) (int, int) { return offset, limit }

// SliceReadResponse slices full content into a paginated ReadResult window.
func SliceReadResponse(fd FileData, offset, limit int) (ReadResult, error) {
	return ReadResult{}, notimpl.Err("backend.SliceReadResponse")
}

// PerformStringReplacement replaces oldString with newString, requiring a
// unique match unless replaceAll. Returns the new content and occurrence count.
func PerformStringReplacement(content, oldString, newString string, replaceAll bool) (string, int, error) {
	return "", 0, notimpl.Err("backend.PerformStringReplacement")
}

// TruncateIfTooLong clips a tool result that exceeds ToolResultTokenLimit.
func TruncateIfTooLong(result string) string { return result }

// SanitizeToolCallID makes a tool call ID safe to use as a path component.
func SanitizeToolCallID(id string) string { return id }

// CompileGrepIncludeGlob compiles a grep --glob filter into a matcher.
func CompileGrepIncludeGlob(pattern string) (func(path string) bool, error) {
	return nil, notimpl.Err("backend.CompileGrepIncludeGlob")
}

// GrepMatchesFromFiles searches in-memory files (StateBackend/StoreBackend grep).
func GrepMatchesFromFiles(files map[string]FileData, pattern string, opts GrepOptions) (GrepResult, error) {
	return GrepResult{}, notimpl.Err("backend.GrepMatchesFromFiles")
}

// GlobSearchFiles matches in-memory file paths against a glob pattern.
func GlobSearchFiles(files map[string]FileData, pattern, path string) (GlobResult, error) {
	return GlobResult{}, notimpl.Err("backend.GlobSearchFiles")
}

// GrepOutputMode selects grep tool output formatting.
type GrepOutputMode string

const (
	GrepFilesWithMatches GrepOutputMode = "files_with_matches"
	GrepContent          GrepOutputMode = "content"
	GrepCount            GrepOutputMode = "count"
)

// FormatGrepMatches renders matches for the grep tool.
func FormatGrepMatches(matches []GrepMatch, mode GrepOutputMode) (string, error) {
	return "", notimpl.Err("backend.FormatGrepMatches")
}

// RegexLiteralHint returns guidance when a literal grep pattern looks like a regex.
func RegexLiteralHint(pattern string) string { return "" }
