package backend

import (
	"context"
	"time"

	"github.com/tsengmingchun/quill/internal/notimpl"
)

// Output limits for sandbox shell-based file operations.
const (
	SandboxMaxOutputBytes = 500 * 1024
	SandboxMaxBinaryBytes = 500 * 1024
)

// SandboxTransport is the minimal surface a remote sandbox provider supplies.
// BaseSandbox builds every file operation on top of it.
type SandboxTransport interface {
	ID() string
	Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse
	UploadFiles(ctx context.Context, files []FileUpload) []FileUploadResponse
	DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse
}

// BaseSandbox implements ls/read/write/edit/grep/glob/delete by running
// shell (python3) commands through a SandboxTransport.
type BaseSandbox struct {
	unimplemented
	transport SandboxTransport
}

// NewBaseSandbox wraps a transport.
func NewBaseSandbox(t SandboxTransport) *BaseSandbox {
	return &BaseSandbox{unimplemented: unimplemented{name: "BaseSandbox"}, transport: t}
}

// ID returns the transport's sandbox identifier.
func (s *BaseSandbox) ID() string { return s.transport.ID() }

// ExecuteWithOffload runs command and captures its full output to a file in
// the sandbox, returning only a preview inline when it exceeds inlineBudget.
func (s *BaseSandbox) ExecuteWithOffload(ctx context.Context, command string, timeout *time.Duration, inlineBudget int) (ExecuteOffloadResult, error) {
	return ExecuteOffloadResult{}, notimpl.Err("backend.BaseSandbox.ExecuteWithOffload")
}

// LangSmithSandbox adapts a LangSmith hosted sandbox.
type LangSmithSandbox struct{ *BaseSandbox }

// NewLangSmithSandbox wraps a LangSmith sandbox transport.
func NewLangSmithSandbox(t SandboxTransport) *LangSmithSandbox {
	b := NewBaseSandbox(t)
	b.name = "LangSmithSandbox"
	return &LangSmithSandbox{BaseSandbox: b}
}
