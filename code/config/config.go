// Package config resolves layered CLI configuration (managed, user, project,
// session, environment) from config.toml and .env files
// (Python `config.py`, `configuration/*`).
package config

import "github.com/tsengmingchun/deepagents_golang/internal/notimpl"

// Scope is a configuration tier, lowest to highest precedence.
type Scope string

const (
	ScopeManaged Scope = "managed"
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
	ScopeSession Scope = "session"
	ScopeEnv     Scope = "env"
)

// Settings is the resolved configuration used by the CLI.
type Settings struct {
	Model              string
	SummarizationModel string
	ReasoningEffort    string
	ApprovalMode       string
	AgentName          string
	ShellAllowList     []string
	Theme              string
	SandboxProvider    string
	WebSearchAPIKey    string
	// Raw holds every resolved key for settings without a dedicated field.
	Raw map[string]any
}

// Source reports one tier's file and its health.
type Source struct {
	Scope Scope
	Path  string
	Err   error
}

// Paths locates CLI directories (~/.deepagents, project .deepagents, ...).
type Paths struct {
	Home       string
	UserDir    string
	ProjectDir string
	ConfigFile string
	SessionsDB string
}

// Resolver builds one process-wide configuration generation and reuses it
// until Reload is called.
type Resolver interface {
	Settings() (*Settings, error)
	Sources() []Source
	Reload() error
}

// DefaultPaths computes paths for the current user and working directory.
func DefaultPaths(cwd string) (Paths, error) {
	return Paths{}, notimpl.Err("config.DefaultPaths")
}

// NewResolver returns a resolver over the given paths.
func NewResolver(p Paths) (Resolver, error) {
	return nil, notimpl.Err("config.NewResolver")
}
