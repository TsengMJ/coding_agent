package backend

import (
	"context"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
)

// unimplemented satisfies Backend, Deleter and the execute half of
// SandboxBackend by reporting "not implemented" in every result. Concrete
// backends embed it and shadow methods as they get implemented.
type unimplemented struct{ name string }

func (u unimplemented) errText(method string) string {
	return notimpl.Err("backend." + u.name + "." + method).Error()
}

func (u unimplemented) Ls(ctx context.Context, path string) LsResult {
	return LsResult{Error: u.errText("Ls")}
}

func (u unimplemented) Read(ctx context.Context, path string, offset, limit int) ReadResult {
	return ReadResult{Error: u.errText("Read")}
}

func (u unimplemented) Write(ctx context.Context, path, content string) WriteResult {
	return WriteResult{Error: u.errText("Write")}
}

func (u unimplemented) Edit(ctx context.Context, path, oldString, newString string, replaceAll bool) EditResult {
	return EditResult{Error: u.errText("Edit")}
}

func (u unimplemented) Delete(ctx context.Context, path string) DeleteResult {
	return DeleteResult{Error: u.errText("Delete")}
}

func (u unimplemented) Grep(ctx context.Context, pattern string, opts GrepOptions) GrepResult {
	return GrepResult{Error: u.errText("Grep")}
}

func (u unimplemented) Glob(ctx context.Context, pattern, path string) GlobResult {
	return GlobResult{Error: u.errText("Glob")}
}

func (u unimplemented) UploadFiles(ctx context.Context, files []FileUpload) []FileUploadResponse {
	out := make([]FileUploadResponse, len(files))
	for i, f := range files {
		out[i] = FileUploadResponse{Path: f.Path, Error: u.errText("UploadFiles")}
	}
	return out
}

func (u unimplemented) DownloadFiles(ctx context.Context, paths []string) []FileDownloadResponse {
	out := make([]FileDownloadResponse, len(paths))
	for i, p := range paths {
		out[i] = FileDownloadResponse{Path: p, Error: u.errText("DownloadFiles")}
	}
	return out
}

func (u unimplemented) execute(method string) ExecuteResponse {
	return ExecuteResponse{Output: u.errText(method)}
}

func (u unimplemented) Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse {
	return u.execute("Execute")
}
