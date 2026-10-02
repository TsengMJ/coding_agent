// Package backend defines pluggable storage and execution backends used by the
// filesystem, skills, memory and summarization middleware
// (Python `deepagents.backends`).
package backend

import (
	"context"
	"time"
)

// Timeouts and limits shared by backends (protocol.py constants).
const (
	DefaultGrepTimeout = 15 * time.Second
	AsyncGrepTimeout   = 2*DefaultGrepTimeout + 5*time.Second
	AsyncGlobTimeout   = 30 * time.Second
	DefaultReadLimit   = 2000
)

// GrepSkippedDirs are dependency directories a disk grep does not descend
// into unless the search path is already inside one.
var GrepSkippedDirs = map[string]bool{"node_modules": true, ".venv": true, "__pycache__": true}

// FileOperationError values are the standardized, LLM-recoverable error codes.
const (
	ErrFileNotFound     = "file_not_found"
	ErrPermissionDenied = "permission_denied"
	ErrIsDirectory      = "is_directory"
	ErrInvalidPath      = "invalid_path"
)

// Encodings used by FileData.
const (
	EncodingUTF8   = "utf-8"
	EncodingBase64 = "base64"
)

// FileInfo is one listing entry. Only Path is guaranteed.
type FileInfo struct {
	Path       string `json:"path"`
	IsDir      bool   `json:"is_dir,omitempty"`
	Size       int64  `json:"size,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

// ContextLine is a non-matching line around a grep match.
type ContextLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// GrepMatch is one grep hit.
type GrepMatch struct {
	Path          string        `json:"path"`
	Line          int           `json:"line"`
	Text          string        `json:"text"`
	ContextBefore []ContextLine `json:"context_before,omitempty"`
	ContextAfter  []ContextLine `json:"context_after,omitempty"`
}

// FileData is stored file content with metadata (the StateBackend "files" value).
type FileData struct {
	Content    string `json:"content"`
	Encoding   string `json:"encoding"`
	CreatedAt  string `json:"created_at,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

// ReadResult is returned by Read. StartLine/EndLine are 1-indexed and set
// together; NextOffset is 0-indexed and equals EndLine when present.
type ReadResult struct {
	Error      string
	FileData   *FileData
	TotalLines *int
	StartLine  *int
	EndLine    *int
	NextOffset *int
	// NoLinesRequested marks a non-positive limit that never inspected the file.
	NoLinesRequested bool
}

// WriteResult is returned by Write.
type WriteResult struct {
	Error string
	Path  string
}

// EditResult is returned by Edit.
type EditResult struct {
	Error       string
	Path        string
	Occurrences int
}

// DeleteResult is returned by Delete.
type DeleteResult struct {
	Error string
	Path  string
}

// LsResult is returned by Ls.
type LsResult struct {
	Error   string
	Entries []FileInfo
}

// GrepResult is returned by Grep. Matches may be partial when Truncated.
type GrepResult struct {
	Error     string
	Matches   []GrepMatch
	Truncated bool
}

// GlobTruncationReason explains why a GlobResult is incomplete.
type GlobTruncationReason string

const (
	GlobTruncatedBudget     GlobTruncationReason = "budget"
	GlobTruncatedUnreadable GlobTruncationReason = "unreadable"
	GlobTruncatedTransport  GlobTruncationReason = "transport"
)

// GlobResult is returned by Glob. Matches may be partial when Truncated.
type GlobResult struct {
	Error            string
	Matches          []FileInfo
	Truncated        bool
	TruncationReason GlobTruncationReason
}

// FileUpload is one file passed to UploadFiles.
type FileUpload struct {
	Path    string
	Content []byte
}

// FileUploadResponse reports one upload; batch operations allow partial success.
type FileUploadResponse struct {
	Path  string
	Error string
}

// FileDownloadResponse reports one download; batch operations allow partial success.
type FileDownloadResponse struct {
	Path    string
	Content []byte
	Error   string
}

// GrepOptions narrows a grep. Pattern matching is literal, not regex.
type GrepOptions struct {
	Path string
	Glob string
	// MaxCount caps total matches; zero means unlimited.
	MaxCount int
	// ContextLines requests surrounding lines (supported by some backends).
	ContextLines int
	// Regexp treats the pattern as an RE2 regular expression instead of
	// literal text (supported by FilesystemBackend; others search literally).
	Regexp bool
}

// Backend is the unified file-storage protocol (`BackendProtocol`).
type Backend interface {
	Ls(ctx context.Context, path string) LsResult
	Read(ctx context.Context, path string, offset, limit int) ReadResult
	Write(ctx context.Context, path, content string) WriteResult
	Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) EditResult
	Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult
	Glob(ctx context.Context, pattern, path string) GlobResult
	UploadFiles(ctx context.Context, files []FileUpload) []FileUploadResponse
	DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse
}

// Deleter is an optional capability; the `delete` tool is offered only when
// the backend implements it (`_supports_delete`).
type Deleter interface {
	Delete(ctx context.Context, path string) DeleteResult
}

// ExecuteResponse is the result of a shell command.
type ExecuteResponse struct {
	Output string
	// ExitCode is nil when the command did not report one (e.g. timed out).
	ExitCode  *int
	Truncated bool
}

// ExecuteArtifact is the structured artifact attached to `execute` tool results.
type ExecuteArtifact struct {
	ExitCode *int   `json:"exit_code,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ExecuteOffloadResult is an execute whose full output was captured to a file.
type ExecuteOffloadResult struct {
	Offloaded                  bool
	Path                       string
	Response                   ExecuteResponse
	PreviewHasTruncationMarker bool
}

// SandboxBackend adds shell execution (`SandboxBackendProtocol`).
type SandboxBackend interface {
	Backend
	ID() string
	// Execute runs command; a nil timeout uses the backend default, 0 may disable it.
	Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse
}

// SupportsExecution reports whether b can run shell commands.
func SupportsExecution(b Backend) bool {
	_, ok := b.(SandboxBackend)
	return ok
}

// SupportsDelete reports whether b implements the optional Deleter capability.
func SupportsDelete(b Backend) bool {
	_, ok := b.(Deleter)
	return ok
}

// ArtifactsRooter is implemented by backends that choose where middleware
// offloads large tool results and messages (CompositeBackend and wrappers).
type ArtifactsRooter interface {
	ArtifactsRoot() string
}

// ArtifactsRoot returns b's artifacts root, or "/" when b does not choose one.
func ArtifactsRoot(b Backend) string {
	if r, ok := b.(ArtifactsRooter); ok && r.ArtifactsRoot() != "" {
		return r.ArtifactsRoot()
	}
	return "/"
}
