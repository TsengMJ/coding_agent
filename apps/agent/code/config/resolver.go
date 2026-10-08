package config

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/tsengmingchun/quill/apps/agent/backend/ossandbox"
)

// Environment variables that override config.toml.
const (
	ModelEnvVar          = "QUILL_MODEL"
	ServerEnvVar         = "QUILL_SERVER"
	ShellAllowListEnvVar = "QUILL_SHELL_ALLOW_LIST"
	// BraveAPIKeyEnvVar enables the web_search tool (shell env or .env).
	BraveAPIKeyEnvVar = "BRAVE_API_KEY"
	// Context7APIKeyEnvVar enables the Context7 documentation tools.
	Context7APIKeyEnvVar = "CONTEXT7_API_KEY"
)

// fileConfig is the part of config.toml with dedicated Settings fields.
type fileConfig struct {
	Models struct {
		Default string `toml:"default"`
		Web     string `toml:"web"`
	} `toml:"models"`
	Storage struct {
		ResultsTTL string `toml:"results_ttl"`
	} `toml:"storage"`
	Startup struct {
		Mode string `toml:"mode"`
	} `toml:"startup"`
	Shell struct {
		// AllowList is a TOML array or a comma-separated string.
		AllowList any `toml:"allow_list"`
	} `toml:"shell"`
	Retries struct {
		MaxRetries *int `toml:"max_retries"`
	} `toml:"retries"`
	Server struct {
		URL string `toml:"url"`
	} `toml:"server"`
	Tracing struct {
		LangSmith bool   `toml:"langsmith"`
		Project   string `toml:"project"`
	} `toml:"tracing"`
	Sandbox sandboxFile `toml:"sandbox"`
}

// fileResolver resolves defaults < config.toml < environment. CLI flags are
// applied by the caller on top of Settings.
type fileResolver struct {
	paths    Paths
	getenv   func(string) string
	settings *Settings
	sources  []Source
}

func (r *fileResolver) Settings() (*Settings, error) {
	s := *r.settings
	return &s, nil
}

func (r *fileResolver) Sources() []Source { return r.sources }

func (r *fileResolver) Reload() error {
	s := &Settings{ApprovalMode: ApprovalYolo, Sandbox: defaultSandboxSettings()}
	src := Source{Scope: ScopeUser, Path: r.paths.ConfigFile}
	err := loadUserFile(r.paths.ConfigFile, s)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil {
		r.sources = []Source{src}
	} else {
		r.sources = nil
	}
	if err := applyEnv(s, r.getenv); err != nil {
		return err
	}
	r.settings = s
	return nil
}

func loadUserFile(path string, s *Settings) error {
	if path == "" {
		return fs.ErrNotExist
	}
	var raw map[string]any
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return fmt.Errorf("config %s: %w", path, err)
	}
	var fc fileConfig
	if _, err := toml.DecodeFile(path, &fc); err != nil {
		return fmt.Errorf("config %s: %w", path, err)
	}
	s.Raw = raw
	s.Model = fc.Models.Default
	s.WebModel = fc.Models.Web
	if v := fc.Storage.ResultsTTL; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("config %s: storage.results_ttl must be a positive duration such as \"168h\", got %q", path, v)
		}
		s.ResultsTTL = d
	}
	s.ServerURL = fc.Server.URL
	s.LangSmithTracing = fc.Tracing.LangSmith
	s.LangSmithProject = fc.Tracing.Project
	s.MaxRetries = fc.Retries.MaxRetries
	if s.MaxRetries != nil && *s.MaxRetries < 0 {
		return fmt.Errorf("config %s: retries.max_retries must be >= 0, got %d", path, *s.MaxRetries)
	}
	if fc.Startup.Mode != "" {
		mode, err := parseApprovalMode(fc.Startup.Mode)
		if err != nil {
			return fmt.Errorf("config %s: startup.mode: %w", path, err)
		}
		s.ApprovalMode = mode
	}
	if err := applyAllowList(s, fc.Shell.AllowList); err != nil {
		return fmt.Errorf("config %s: shell.allow_list: %w", path, err)
	}
	if err := applySandbox(&s.Sandbox, fc.Sandbox); err != nil {
		return fmt.Errorf("config %s: sandbox.%w", path, err)
	}
	return nil
}

func parseApprovalMode(v string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(v)); mode {
	case ApprovalManual, ApprovalYolo:
		return mode, nil
	case "auto":
		return "", errors.New(`"auto" (classifier approval) is not supported yet; use "manual" or "yolo"`)
	default:
		return "", fmt.Errorf("want \"manual\" or \"yolo\", got %q", v)
	}
}

func applyAllowList(s *Settings, v any) error {
	var (
		cmds []string
		all  bool
		err  error
	)
	switch list := v.(type) {
	case nil:
		return nil
	case string:
		cmds, all, err = ParseShellAllowList(list)
	case []any:
		items := make([]string, 0, len(list))
		for _, item := range list {
			str, ok := item.(string)
			if !ok {
				return fmt.Errorf("entries must be strings, got %T", item)
			}
			items = append(items, str)
		}
		cmds, all, err = ParseShellAllowListItems(items)
	default:
		return fmt.Errorf("want a string or an array of strings, got %T", v)
	}
	if err != nil {
		return err
	}
	s.ShellAllowList, s.ShellAllowAll = cmds, all
	return nil
}

func applyEnv(s *Settings, getenv func(string) string) error {
	if v := getenv(ModelEnvVar); v != "" {
		s.Model = v
	}
	if v := getenv(ServerEnvVar); v != "" {
		s.ServerURL = v
	}
	s.WebSearchAPIKey = getenv(BraveAPIKeyEnvVar)
	s.Context7APIKey = getenv(Context7APIKeyEnvVar)
	if v := getenv(ShellAllowListEnvVar); v != "" {
		if err := applyAllowList(s, v); err != nil {
			return fmt.Errorf("$%s: %w", ShellAllowListEnvVar, err)
		}
	}
	if v := getenv(SandboxModeEnvVar); v != "" {
		m, err := ossandbox.ParseMode(v)
		if err != nil {
			return fmt.Errorf("$%s: %w", SandboxModeEnvVar, err)
		}
		s.Sandbox.Mode = m
	}
	return nil
}
