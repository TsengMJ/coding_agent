// Package config resolves layered CLI configuration (managed, user, project,
// session, environment) from config.toml and .env files
// (Python `config.py`, `configuration/*`).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
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
	// Model is the default "provider:model" (models.default, $QUILL_MODEL).
	Model string
	// WebModel ("provider:model", models.web) reads fetched pages and runs
	// the web-researcher; empty uses the run's model.
	WebModel           string
	SummarizationModel string
	ReasoningEffort    string
	// ApprovalMode is ApprovalManual or ApprovalYolo (startup.mode, default yolo).
	ApprovalMode string
	AgentName    string
	// ShellAllowList names commands that run without approval
	// (shell.allow_list, $QUILL_SHELL_ALLOW_LIST).
	ShellAllowList []string
	// ShellAllowAll approves every command (shell.allow_list = "all").
	ShellAllowAll bool
	// MaxRetries overrides llm.DefaultMaxRetries when non-nil (retries.max_retries).
	MaxRetries *int
	// ServerURL is the `quill serve` URL clients use (server.url, $QUILL_SERVER).
	ServerURL       string
	Theme           string
	SandboxProvider string
	// WebSearchAPIKey is the Brave Search API key ($BRAVE_API_KEY); empty
	// leaves web_search out.
	WebSearchAPIKey string
	// Context7APIKey ($CONTEXT7_API_KEY) enables resolve_library_id and
	// query_docs.
	Context7APIKey string
	// ResultsTTL is how long evicted tool results stay readable
	// (storage.results_ttl); zero uses the default.
	ResultsTTL time.Duration
	// LangSmithTracing sends run trees to LangSmith (tracing.langsmith,
	// default false; LANGSMITH_TRACING=true also turns it on).
	LangSmithTracing bool
	// LangSmithProject is the LangSmith project for quill's runs
	// (tracing.project); empty uses $LANGSMITH_PROJECT, then "quill".
	LangSmithProject string
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

// Paths locates CLI directories (~/.quill, project .quill, ...).
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
const DirName = ".quill"

// ConfigFileName is the user configuration file inside the user DirName.
const ConfigFileName = "config.toml"

// DefaultPaths computes paths for the current user and working directory.
// Profile-specific state directories are not modeled yet; everything lives
// directly under ~/.quill.
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
		ConfigFile:   filepath.Join(userDir, ConfigFileName),
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
