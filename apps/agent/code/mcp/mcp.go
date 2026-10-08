// Package mcp loads MCP server configs, manages OAuth and exposes MCP tools
// to the agent (Python `mcp_config.py`, `mcp_tools.py`, `mcp_auth.py`).
package mcp

import (
	"context"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// Transport is how the CLI connects to an MCP server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
)

// ServerConfig is one entry of the MCP config file.
type ServerConfig struct {
	Name      string            `json:"name"`
	Transport Transport         `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Disabled  bool              `json:"disabled,omitempty"`
}

// LoadConfigs merges user and project MCP config files.
func LoadConfigs(paths ...string) ([]ServerConfig, error) {
	return nil, notimpl.Err("mcp.LoadConfigs")
}

// Session is a live connection to MCP servers.
type Session interface {
	Tools(ctx context.Context) ([]tool.Tool, error)
	Close() error
}

// Connect starts or connects to every enabled server.
func Connect(ctx context.Context, cfgs []ServerConfig) (Session, error) {
	return nil, notimpl.Err("mcp.Connect")
}

// TokenStore persists OAuth tokens for HTTP MCP servers.
type TokenStore interface {
	Load(server string) (map[string]any, error)
	Save(server string, token map[string]any) error
	Delete(server string) error
}
