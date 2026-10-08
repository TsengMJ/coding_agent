package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"

	"github.com/tsengmingchun/quill/apps/agent/backend"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
)

// Artifact directories under ArtifactsRoot.
const (
	ResultsDir = ArtifactsRoot + "/large_tool_results"
	WebDir     = ArtifactsRoot + "/web"
	HistoryDir = HistoryPathPrefix
)

// Artifacts are the stores behind the artifact directories, so offloaded
// content lives in Redis or Postgres rather than in the project tree. A nil
// store leaves its directory on the Backend.
type Artifacts struct {
	// Results holds evicted large tool results; items may expire.
	Results rt.Store
	// Web holds full fetched pages; items may expire.
	Web rt.Store
	// History holds conversation history moved out by summarization.
	History rt.Store
}

const expiredHint = "the saved output has expired; run the tool again to regenerate it"

// projectKey names a project in store namespaces.
func projectKey(cwd string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(cwd)))
	return hex.EncodeToString(sum[:8])
}

// artifactRoutes maps each artifact directory with a store to a
// StoreBackend namespaced by project and directory.
func artifactRoutes(cwd string, a *Artifacts) map[string]backend.Backend {
	routes := map[string]backend.Backend{}
	if a == nil {
		return routes
	}
	key := projectKey(cwd)
	add := func(dir, kind string, s rt.Store, hint string) {
		if s == nil {
			return
		}
		b := backend.NewStoreBackend(backend.StaticNamespace(rt.Namespace{"quill", key, kind}), s)
		if hint != "" {
			b = b.WithNotFoundHint(hint)
		}
		routes[dir+"/"] = b
	}
	add(ResultsDir, "large_tool_results", a.Results, expiredHint)
	add(WebDir, "web", a.Web, expiredHint)
	add(HistoryDir, "conversation_history", a.History, "")
	return routes
}

// WebPages returns the backend that holds cwd's fetched pages, with paths
// relative to WebDir, or nil when pages stay on the Backend.
func (a *Artifacts) WebPages(cwd string) backend.Backend {
	if a == nil || a.Web == nil {
		return nil
	}
	return artifactRoutes(cwd, &Artifacts{Web: a.Web})[WebDir+"/"]
}

// NewProjectBackend is NewBackend(cwd) with the artifact directories routed
// to a's stores.
func NewProjectBackend(cwd string, a *Artifacts) backend.Backend {
	return withHostRoutes(NewBackend(cwd), nil, a, cwd)
}
