package backend

// FilesystemOptions configures FilesystemBackend.
type FilesystemOptions struct {
	// RootDir defaults to the current working directory.
	RootDir string
	// VirtualMode treats RootDir as "/" and blocks escapes (default true in Python).
	VirtualMode bool
	// MaxFileSizeMB caps readable file size (default 10).
	MaxFileSizeMB int
}

// FilesystemBackend reads and writes the local disk, using ripgrep for grep
// when available.
type FilesystemBackend struct {
	unimplemented
	opts FilesystemOptions
}

// NewFilesystemBackend returns a local-disk backend.
func NewFilesystemBackend(opts FilesystemOptions) *FilesystemBackend {
	return &FilesystemBackend{unimplemented: unimplemented{name: "FilesystemBackend"}, opts: opts}
}
