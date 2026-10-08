// Package mcp loads MCP server configs and exposes MCP tools to the agent
// (Python `mcp_config.py`, `mcp_tools.py`). Only the stdio transport is
// supported so far.
package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// Transport is how the CLI connects to an MCP server.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
	TransportSSE   Transport = "sse"
)

// ToolPrefix starts the name of every MCP tool: mcp__<server>__<tool>.
const ToolPrefix = "mcp__"

// ServerConfig is one entry of the MCP config file.
type ServerConfig struct {
	Name      string            `json:"name"`
	Transport Transport         `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	// Dir is the server's working directory; empty inherits quill's.
	Dir      string            `json:"dir,omitempty"`
	URL      string            `json:"url,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Disabled bool              `json:"disabled,omitempty"`
}

// configFile is the Claude Desktop / Cursor style MCP config file.
type configFile struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// Server names become part of tool names, which providers restrict to
// [a-zA-Z0-9_-].
var serverNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// LoadConfigs merges MCP config files; a server in a later file replaces
// one of the same name in an earlier file. Missing files are skipped. The
// result is sorted by name.
func LoadConfigs(paths ...string) ([]ServerConfig, error) {
	merged := map[string]ServerConfig{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("mcp config %s: %w", p, err)
		}
		var f configFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("mcp config %s: %w", p, err)
		}
		for name, c := range f.MCPServers {
			c.Name = name
			if c.Transport == "" {
				c.Transport = TransportStdio
			}
			if err := validate(c); err != nil {
				return nil, fmt.Errorf("mcp config %s: server %q: %w", p, name, err)
			}
			merged[name] = c
		}
	}
	out := make([]ServerConfig, 0, len(merged))
	for _, name := range slices.Sorted(maps.Keys(merged)) {
		out = append(out, merged[name])
	}
	return out, nil
}

func validate(c ServerConfig) error {
	if !serverNameRe.MatchString(c.Name) {
		return errors.New("name may only contain letters, digits, '_' and '-'")
	}
	switch c.Transport {
	case TransportStdio:
		if c.Command == "" {
			return errors.New("stdio server needs a command")
		}
	case TransportHTTP, TransportSSE:
		if c.URL == "" {
			return fmt.Errorf("%s server needs a url", c.Transport)
		}
	default:
		return fmt.Errorf("unknown transport %q (want stdio, http or sse)", c.Transport)
	}
	return nil
}

// Session is a live connection to MCP servers.
type Session interface {
	Tools(ctx context.Context) ([]tool.Tool, error)
	Close() error
}

// Connect starts every enabled server and completes the MCP handshake. If
// any server fails, the ones already started are closed.
func Connect(ctx context.Context, cfgs []ServerConfig) (Session, error) {
	transports := map[string]sdk.Transport{}
	for _, c := range cfgs {
		if c.Disabled {
			continue
		}
		if c.Transport != TransportStdio {
			return nil, fmt.Errorf("mcp server %q: transport %s is not supported yet", c.Name, c.Transport)
		}
		cmd := exec.Command(c.Command, c.Args...)
		cmd.Env = os.Environ()
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Dir = c.Dir
		cmd.Stderr = os.Stderr
		transports[c.Name] = &sdk.CommandTransport{Command: cmd}
	}
	return connectTransports(ctx, transports)
}

func connectTransports(ctx context.Context, transports map[string]sdk.Transport) (Session, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "quill", Version: "dev"}, nil)
	s := &session{}
	for _, name := range slices.Sorted(maps.Keys(transports)) {
		cs, err := client.Connect(ctx, transports[name], nil)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("mcp server %q: %w", name, err)
		}
		s.servers = append(s.servers, server{name: name, cs: cs})
	}
	return s, nil
}

type server struct {
	name string
	cs   *sdk.ClientSession
}

type session struct {
	servers []server
}

func (s *session) Tools(ctx context.Context) ([]tool.Tool, error) {
	var out []tool.Tool
	for _, srv := range s.servers {
		for t, err := range srv.cs.Tools(ctx, nil) {
			if err != nil {
				return nil, fmt.Errorf("mcp server %q: list tools: %w", srv.name, err)
			}
			out = append(out, &mcpTool{server: srv, tool: t})
		}
	}
	return out, nil
}

func (s *session) Close() error {
	var errs []error
	for _, srv := range s.servers {
		if err := srv.cs.Close(); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", srv.name, err))
		}
	}
	return errors.Join(errs...)
}

type mcpTool struct {
	server server
	tool   *sdk.Tool
}

func (t *mcpTool) Name() string        { return ToolPrefix + t.server.name + "__" + t.tool.Name }
func (t *mcpTool) Description() string { return t.tool.Description }

func (t *mcpTool) Schema() tool.Schema {
	if m, ok := t.tool.InputSchema.(map[string]any); ok {
		return m
	}
	return tool.Schema{"type": "object"}
}

// Invoke reports transport failures (server crashed or closed) as an error
// result, so the model sees them and the run goes on.
func (t *mcpTool) Invoke(ctx context.Context, _ *tool.Runtime, args map[string]any) (*tool.Result, error) {
	if args == nil {
		args = map[string]any{}
	}
	res, err := t.server.cs.CallTool(ctx, &sdk.CallToolParams{Name: t.tool.Name, Arguments: args})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := fmt.Sprintf("MCP server %q failed to run %s: %v", t.server.name, t.tool.Name, err)
		return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(msg)}}, nil
	}
	return &tool.Result{IsError: res.IsError, Content: contentBlocks(res)}, nil
}

func contentBlocks(res *sdk.CallToolResult) []llm.ContentBlock {
	var out []llm.ContentBlock
	for _, c := range res.Content {
		switch c := c.(type) {
		case *sdk.TextContent:
			out = append(out, llm.TextBlock(c.Text))
		case *sdk.ImageContent:
			out = append(out, llm.ContentBlock{Type: llm.BlockImage, MimeType: c.MIMEType, Base64: base64.StdEncoding.EncodeToString(c.Data)})
		case *sdk.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				out = append(out, llm.TextBlock(c.Resource.Text))
			} else if c.Resource != nil {
				out = append(out, llm.TextBlock(fmt.Sprintf("[resource %s (%s)]", c.Resource.URI, c.Resource.MIMEType)))
			}
		case *sdk.ResourceLink:
			out = append(out, llm.TextBlock(fmt.Sprintf("[resource link %s: %s]", c.Name, c.URI)))
		default:
			out = append(out, llm.TextBlock(fmt.Sprintf("[unsupported MCP content %T]", c)))
		}
	}
	if len(out) == 0 && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			out = append(out, llm.TextBlock(string(data)))
		}
	}
	if len(out) == 0 {
		out = append(out, llm.TextBlock("(no output)"))
	}
	return out
}

// IsToolName reports whether name belongs to an MCP tool.
func IsToolName(name string) bool { return strings.HasPrefix(name, ToolPrefix) }

// ToolAllowed reports whether an MCP tool runs without approval: allow holds
// full tool names or path.Match patterns such as "mcp__devtools__*".
func ToolAllowed(name string, allow []string) bool {
	for _, pattern := range allow {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			return true
		}
	}
	return false
}

// TokenStore persists OAuth tokens for HTTP MCP servers.
type TokenStore interface {
	Load(server string) (map[string]any, error)
	Save(server string, token map[string]any) error
	Delete(server string) error
}
