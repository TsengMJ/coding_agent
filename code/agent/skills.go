package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tsengmingchun/quill/backend"
	"github.com/tsengmingchun/quill/middleware/skills"
)

// SkillSources lists quill's skill directories, lowest priority first: the
// user agent's skills and ~/.agents/skills (host paths), then the project's
// /.quill/skills and /.agents/skills (virtual paths). userDir is
// ~/.quill; empty skips the user-level sources.
func SkillSources(userDir, agentName string) []skills.Source {
	return append(userSkillSources(userDir, agentName),
		skills.Source{Path: "/.quill/skills", Label: "Project Quill"},
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
		{Path: filepath.Join(userDir, agentName, "skills"), Label: "User Quill"},
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

// ArtifactsRoot is where quill offloads large tool results and messages,
// next to /.quill/conversation_history.
const ArtifactsRoot = "/.quill"

// withHostRoutes mounts each existing host directory in roots as a read-only
// CompositeBackend route at its own host path, so project file tools can
// read (never write) user-level skills, and routes the artifact directories
// to a's stores. Every other path, and Execute, stay on b. Large-result
// eviction writes under ArtifactsRoot.
func withHostRoutes(b backend.Backend, roots []string, a *Artifacts, cwd string) backend.Backend {
	routes := artifactRoutes(cwd, a)
	for _, root := range roots {
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		prefix := filepath.ToSlash(root) + "/"
		fs := backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: root, VirtualMode: true})
		routes[prefix] = readOnlyRoute{Backend: fs, prefix: strings.TrimSuffix(prefix, "/")}
	}
	return backend.NewCompositeBackend(b, routes, ArtifactsRoot)
}

// readOnlyRoute refuses mutations; prefix restores the caller's path in errors.
type readOnlyRoute struct {
	backend.Backend
	prefix string
}

func (r readOnlyRoute) readOnlyError(p string) string {
	return fmt.Sprintf("Error: '%s' is a read-only skill directory", r.prefix+p)
}

func (r readOnlyRoute) Write(ctx context.Context, p, content string) backend.WriteResult {
	return backend.WriteResult{Error: r.readOnlyError(p)}
}

func (r readOnlyRoute) Edit(ctx context.Context, p, oldString, newString string, replaceAll bool) backend.EditResult {
	return backend.EditResult{Error: r.readOnlyError(p)}
}

func (r readOnlyRoute) Delete(ctx context.Context, p string) backend.DeleteResult {
	return backend.DeleteResult{Error: r.readOnlyError(p)}
}

func (r readOnlyRoute) UploadFiles(ctx context.Context, files []backend.FileUpload) []backend.FileUploadResponse {
	out := make([]backend.FileUploadResponse, len(files))
	for i, f := range files {
		out[i] = backend.FileUploadResponse{Path: f.Path, Error: r.readOnlyError(f.Path)}
	}
	return out
}
