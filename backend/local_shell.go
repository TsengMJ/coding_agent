package backend

import "time"

// DefaultExecuteTimeout is LocalShellBackend's default command timeout.
const DefaultExecuteTimeout = 120 * time.Second

// LocalShellOptions configures LocalShellBackend.
type LocalShellOptions struct {
	FilesystemOptions
	Timeout        time.Duration
	MaxOutputBytes int
	Env            map[string]string
	// InheritEnv passes the parent process environment to commands.
	InheritEnv bool
}

// LocalShellBackend is a FilesystemBackend that also runs shell commands on
// the host. It provides no isolation.
type LocalShellBackend struct {
	*FilesystemBackend
	opts LocalShellOptions
}

// NewLocalShellBackend returns a host shell backend.
func NewLocalShellBackend(opts LocalShellOptions) *LocalShellBackend {
	fs := &FilesystemBackend{unimplemented: unimplemented{name: "LocalShellBackend"}, opts: opts.FilesystemOptions}
	return &LocalShellBackend{FilesystemBackend: fs, opts: opts}
}

// ID identifies this backend instance.
func (b *LocalShellBackend) ID() string { return "local-shell" }
