// Package plugins installs and loads plugins that bundle skills, hooks and
// MCP servers from marketplaces (Python `plugins/*`).
package plugins

import "github.com/tsengmingchun/deepagents_golang/internal/notimpl"

// Manifest is a plugin's plugin.json.
type Manifest struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Skills      []string `json:"skills,omitempty"`
	Hooks       string   `json:"hooks,omitempty"`
	MCPServers  string   `json:"mcp_servers,omitempty"`
}

// Store tracks installed and enabled plugins.
type Store interface {
	List() ([]Manifest, error)
	Install(source string) (*Manifest, error)
	Uninstall(name string) error
	SetEnabled(name string, enabled bool) error
}

// OpenStore opens the plugin store rooted at dir.
func OpenStore(dir string) (Store, error) {
	return nil, notimpl.Err("plugins.OpenStore")
}
