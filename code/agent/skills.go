package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

// ArtifactsRoot is where dcode offloads large tool results and messages,
// next to /.deepagents/conversation_history.
const ArtifactsRoot = "/.deepagents"

// withHostRoutes mounts each existing host directory in roots as a read-only
// CompositeBackend route at its own host path, so project file tools can
// read (never write) user-level skills. Every other path, and Execute, stay
// on b. Large-result eviction writes under ArtifactsRoot.
func withHostRoutes(b backend.Backend, roots []string) backend.Backend {
	routes := map[string]backend.Backend{}
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
