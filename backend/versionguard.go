package backend

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path"
	"sync"
	"time"
)

// VersionGuard refuses to change an existing file unless the version on
// disk is the one last read or written through it. That enforces
// read-before-edit and stops an edit based on a stale read from landing on
// a file that a command, the user or another editor changed since.
type VersionGuard struct {
	Backend
	mu   sync.Mutex
	seen map[string][sha256.Size]byte
}

// WithVersionGuard wraps b, keeping its optional capabilities (execution,
// delete). Wrapping a guarded backend again returns it unchanged.
func WithVersionGuard(b Backend) Backend {
	if isGuarded(b) {
		return b
	}
	g := &VersionGuard{Backend: b, seen: map[string][sha256.Size]byte{}}
	sb, isSandbox := b.(SandboxBackend)
	d, isDeleter := b.(Deleter)
	switch {
	case isSandbox && isDeleter:
		return &struct {
			*VersionGuard
			guardSandbox
			guardDeleter
		}{g, guardSandbox{sb}, guardDeleter{g, d}}
	case isSandbox:
		return &struct {
			*VersionGuard
			guardSandbox
		}{g, guardSandbox{sb}}
	case isDeleter:
		return &struct {
			*VersionGuard
			guardDeleter
		}{g, guardDeleter{g, d}}
	}
	return g
}

type guarded interface{ versionGuard() *VersionGuard }

func (g *VersionGuard) versionGuard() *VersionGuard { return g }

func isGuarded(b Backend) bool {
	_, ok := b.(guarded)
	return ok
}

func (g *VersionGuard) ArtifactsRoot() string { return ArtifactsRoot(g.Backend) }

func (g *VersionGuard) Read(ctx context.Context, p string, offset, limit int) ReadResult {
	res := g.Backend.Read(ctx, p, offset, limit)
	if res.Error == "" {
		g.remember(ctx, p)
	}
	return res
}

func (g *VersionGuard) Write(ctx context.Context, p, content string) WriteResult {
	if msg := g.check(ctx, p); msg != "" {
		return WriteResult{Error: msg}
	}
	res := g.Backend.Write(ctx, p, content)
	if res.Error == "" {
		g.remember(ctx, p)
	}
	return res
}

func (g *VersionGuard) Edit(ctx context.Context, p, oldString, newString string, replaceAll bool) EditResult {
	if msg := g.check(ctx, p); msg != "" {
		return EditResult{Error: msg}
	}
	res := g.Backend.Edit(ctx, p, oldString, newString, replaceAll)
	if res.Error == "" {
		g.remember(ctx, p)
	}
	return res
}

// check returns why p may not be changed, or "" when it may: p does not
// exist yet, or its content is the version last seen.
func (g *VersionGuard) check(ctx context.Context, p string) string {
	sum, exists := g.current(ctx, p)
	if !exists {
		return ""
	}
	g.mu.Lock()
	seen, ok := g.seen[path.Clean(p)]
	g.mu.Unlock()
	switch {
	case !ok:
		return fmt.Sprintf("Error: %s has not been read yet. Read it with read_file before changing it.", p)
	case seen != sum:
		return fmt.Sprintf("Error: %s changed since you last read it (by a command, the user or another editor). Read it again before changing it.", p)
	}
	return ""
}

func (g *VersionGuard) remember(ctx context.Context, p string) {
	sum, exists := g.current(ctx, p)
	g.mu.Lock()
	defer g.mu.Unlock()
	if exists {
		g.seen[path.Clean(p)] = sum
	} else {
		delete(g.seen, path.Clean(p))
	}
}

func (g *VersionGuard) current(ctx context.Context, p string) ([sha256.Size]byte, bool) {
	res := g.Backend.DownloadFiles(ctx, []string{p})
	if len(res) != 1 || res[0].Error != "" {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(res[0].Content), true
}

type guardSandbox struct{ sb SandboxBackend }

func (s guardSandbox) ID() string { return s.sb.ID() }

func (s guardSandbox) Execute(ctx context.Context, command string, timeout *time.Duration) ExecuteResponse {
	return s.sb.Execute(ctx, command, timeout)
}

type guardDeleter struct {
	g *VersionGuard
	d Deleter
}

func (d guardDeleter) Delete(ctx context.Context, p string) DeleteResult {
	res := d.d.Delete(ctx, p)
	if res.Error == "" {
		d.g.remember(ctx, p)
	}
	return res
}
