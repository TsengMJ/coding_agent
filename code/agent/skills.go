package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/middleware/skills"
)

// SkillSources lists dcode's skill directories, lowest priority first: the
// user agent's skills and ~/.agents/skills (host paths), then the project's
// /.deepagents/skills and /.agents/skills (virtual paths). userDir is
// ~/.deepagents; empty skips the user-level sources.
func SkillSources(userDir, agentName string) []skills.Source {
	return append(userSkillSources(userDir, agentName),
		skills.Source{Path: "/.deepagents/skills", Label: "Project Deepagents"},
		skills.Source{Path: "/.agents/skills", Label: "Project Agents"},
	)
}

func userSkillSources(userDir, agentName string) []skills.Source {
	if userDir == "" {
		return nil
	}
	if agentName == "" {
		agentName = DefaultAgentName
	}
	return []skills.Source{
		{Path: filepath.Join(userDir, agentName, "skills"), Label: "User Deepagents"},
		{Path: filepath.Join(filepath.Dir(userDir), ".agents", "skills"), Label: "User Agents"},
	}
}

// existingSkillSources drops sources whose directory b reports missing, so
// the prompt does not warn about skill directories the user never created.
func existingSkillSources(ctx context.Context, b backend.Backend, srcs []skills.Source) []skills.Source {
	var out []skills.Source
	for _, s := range srcs {
		if !strings.HasSuffix(b.Ls(ctx, s.Path).Error, "path_not_found") {
			out = append(out, s)
		}
	}
	return out
}

// userSkillRoots returns the host directories holding user-level skills.
func userSkillRoots(userDir, agentName string) []string {
	var out []string
	for _, s := range userSkillSources(userDir, agentName) {
		out = append(out, s.Path)
	}
	return out
}

// withHostRoutes lets project file tools read (never write) the given host
// directories: Ls / Read / Glob / Grep / DownloadFiles under a root go to the
// host filesystem, and Write / Edit / Delete there are refused. Every other
// path, and Execute, stay on b.
func withHostRoutes(b backend.Backend, roots []string) backend.Backend {
	if len(roots) == 0 {
		return b
	}
	r := &hostRoutes{Backend: b, host: backend.NewFilesystemBackend(backend.FilesystemOptions{}), roots: roots}
	sb, isSandbox := b.(backend.SandboxBackend)
	d, isDeleter := b.(backend.Deleter)
	switch {
	case isSandbox && isDeleter:
		return &struct {
			hostSandbox
			hostDeleter
		}{hostSandbox{r, sb}, hostDeleter{r, d}}
	case isSandbox:
		return &hostSandbox{r, sb}
	case isDeleter:
		return &struct {
			*hostRoutes
			hostDeleter
		}{r, hostDeleter{r, d}}
	default:
		return r
	}
}

type hostRoutes struct {
	backend.Backend
	host  backend.Backend
	roots []string
}

func (r *hostRoutes) onHost(p string) bool {
	return slices.ContainsFunc(r.roots, func(root string) bool {
		return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
	})
}

func readOnlyError(p string) string {
	return fmt.Sprintf("Error: '%s' is a read-only skill directory", p)
}

func (r *hostRoutes) Ls(ctx context.Context, p string) backend.LsResult {
	if r.onHost(p) {
		return r.host.Ls(ctx, p)
	}
	return r.Backend.Ls(ctx, p)
}

func (r *hostRoutes) Read(ctx context.Context, p string, offset, limit int) backend.ReadResult {
	if r.onHost(p) {
		return r.host.Read(ctx, p, offset, limit)
	}
	return r.Backend.Read(ctx, p, offset, limit)
}

func (r *hostRoutes) Glob(ctx context.Context, pattern, p string) backend.GlobResult {
	if r.onHost(p) {
		return r.host.Glob(ctx, pattern, p)
	}
	return r.Backend.Glob(ctx, pattern, p)
}

func (r *hostRoutes) Grep(ctx context.Context, pattern string, opts backend.GrepOptions) backend.GrepResult {
	if r.onHost(opts.Path) {
		return r.host.Grep(ctx, pattern, opts)
	}
	return r.Backend.Grep(ctx, pattern, opts)
}

func (r *hostRoutes) Write(ctx context.Context, p, content string) backend.WriteResult {
	if r.onHost(p) {
		return backend.WriteResult{Error: readOnlyError(p)}
	}
	return r.Backend.Write(ctx, p, content)
}

func (r *hostRoutes) Edit(ctx context.Context, p, oldString, newString string, replaceAll bool) backend.EditResult {
	if r.onHost(p) {
		return backend.EditResult{Error: readOnlyError(p)}
	}
	return r.Backend.Edit(ctx, p, oldString, newString, replaceAll)
}

func (r *hostRoutes) DownloadFiles(ctx context.Context, paths []string) []backend.FileDownloadResponse {
	out := make([]backend.FileDownloadResponse, len(paths))
	for i, p := range paths {
		src := r.Backend
		if r.onHost(p) {
			src = r.host
		}
		out[i] = src.DownloadFiles(ctx, []string{p})[0]
	}
	return out
}

type hostSandbox struct {
	*hostRoutes
	sb backend.SandboxBackend
}

func (s *hostSandbox) ID() string { return s.sb.ID() }

func (s *hostSandbox) Execute(ctx context.Context, command string, timeout *time.Duration) backend.ExecuteResponse {
	return s.sb.Execute(ctx, command, timeout)
}

type hostDeleter struct {
	r *hostRoutes
	d backend.Deleter
}

func (h hostDeleter) Delete(ctx context.Context, p string) backend.DeleteResult {
	if h.r.onHost(p) {
		return backend.DeleteResult{Error: readOnlyError(p)}
	}
	return h.d.Delete(ctx, p)
}
