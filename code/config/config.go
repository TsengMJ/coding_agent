// Package config resolves layered CLI configuration (managed, user, project,
// session, environment) from config.toml and .env files
// (Python `config.py`, `configuration/*`).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

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
	// Model is the default "provider:model" (models.default, $DCODE_MODEL).
	Model              string
	SummarizationModel string
	ReasoningEffort    string
	// ApprovalMode is ApprovalManual or ApprovalYolo (startup.mode, default yolo).
	ApprovalMode string
	AgentName    string
	// ShellAllowList names commands that run without approval
	// (shell.allow_list, $DEEPAGENTS_CODE_SHELL_ALLOW_LIST).
	ShellAllowList []string
	// ShellAllowAll approves every command (shell.allow_list = "all").
	ShellAllowAll bool
	// MaxRetries overrides llm.DefaultMaxRetries when non-nil (retries.max_retries).
	MaxRetries *int
	// ServerURL is the `dcode serve` URL clients use (server.url, $DCODE_SERVER).
	ServerURL       string
	Theme           string
	SandboxProvider string
	// WebSearchAPIKey is the Brave Search API key ($BRAVE_API_KEY); empty
	// leaves web_search out.
	WebSearchAPIKey string
	// Raw holds every key read from config.toml.
	Raw map[string]any
}

// Approval modes for startup.mode.
const (
	ApprovalManual = "manual"
	ApprovalYolo   = "yolo"
)

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
	// GlobalDotEnv is the user-wide .env, loaded below the project .env.
	GlobalDotEnv string
}

// Resolver builds one process-wide configuration generation and reuses it
// until Reload is called.
type Resolver interface {
	Settings() (*Settings, error)
	Sources() []Source
	Reload() error
}

// DirName is the per-user and per-project configuration directory name.
const DirName = ".deepagents"

// DefaultPaths computes paths for the current user and working directory.
// Profile-specific state directories are not modeled yet; everything lives
// directly under ~/.deepagents.
func DefaultPaths(cwd string) (Paths, error) {
	if cwd == "" {
		return Paths{}, errors.New("config.DefaultPaths: cwd is required")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config.DefaultPaths: %w", err)
	}
	userDir := filepath.Join(home, DirName)
	return Paths{
		Home:         home,
		UserDir:      userDir,
		ProjectDir:   filepath.Join(cwd, DirName),
		ConfigFile:   filepath.Join(userDir, "config.toml"),
		SessionsDB:   filepath.Join(userDir, "sessions.db"),
		GlobalDotEnv: filepath.Join(userDir, ".env"),
	}, nil
}

// NewResolver returns a resolver over the user config file and the process
// environment. Managed and project tiers are not read yet.
func NewResolver(p Paths) (Resolver, error) {
	r := &fileResolver{paths: p, getenv: os.Getenv}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}
