package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) Paths {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Paths{UserDir: dir, ConfigFile: path}
}

func resolve(t *testing.T, p Paths) *Settings {
	t.Helper()
	r, err := NewResolver(p)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Settings()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func clearEnv(t *testing.T) {
	for _, k := range []string{ModelEnvVar, ServerEnvVar, ShellAllowListEnvVar, BraveAPIKeyEnvVar, Context7APIKeyEnvVar} {
		t.Setenv(k, "")
	}
}

func TestResolverMissingFileGivesDefaults(t *testing.T) {
	clearEnv(t)
	s := resolve(t, Paths{ConfigFile: filepath.Join(t.TempDir(), "absent.toml")})
	if s.ApprovalMode != ApprovalYolo || s.Model != "" || s.MaxRetries != nil || s.ShellAllowList != nil {
		t.Errorf("settings = %+v", s)
	}
}

func TestResolverTracingIsOffByDefault(t *testing.T) {
	clearEnv(t)
	if s := resolve(t, writeConfig(t, "")); s.LangSmithTracing || s.LangSmithProject != "" {
		t.Errorf("settings = %+v", s)
	}
	s := resolve(t, writeConfig(t, "[tracing]\nlangsmith = true\nproject = \"quill-dev\"\n"))
	if !s.LangSmithTracing || s.LangSmithProject != "quill-dev" {
		t.Errorf("settings = %+v", s)
	}
}

func TestResolverManualModeTurnsReviewBackOn(t *testing.T) {
	clearEnv(t)
	s := resolve(t, writeConfig(t, "[startup]\nmode = \"manual\"\n"))
	if s.ApprovalMode != ApprovalManual {
		t.Errorf("ApprovalMode = %q", s.ApprovalMode)
	}
}

func TestResolverReadsUserFile(t *testing.T) {
	clearEnv(t)
	s := resolve(t, writeConfig(t, `
[models]
default = "openai:gpt-5"

[startup]
mode = "yolo"

[shell]
allow_list = ["ls", "make"]

[retries]
max_retries = 0

[server]
url = "http://127.0.0.1:8123"

[ui]
theme = "dark"
`))
	if s.Model != "openai:gpt-5" {
		t.Errorf("Model = %q", s.Model)
	}
	if s.ApprovalMode != ApprovalYolo {
		t.Errorf("ApprovalMode = %q", s.ApprovalMode)
	}
	if !slices.Equal(s.ShellAllowList, []string{"ls", "make"}) {
		t.Errorf("ShellAllowList = %v", s.ShellAllowList)
	}
	if s.MaxRetries == nil || *s.MaxRetries != 0 {
		t.Errorf("MaxRetries = %v", s.MaxRetries)
	}
	if s.ServerURL != "http://127.0.0.1:8123" {
		t.Errorf("ServerURL = %q", s.ServerURL)
	}
	if ui, _ := s.Raw["ui"].(map[string]any); ui["theme"] != "dark" {
		t.Errorf("Raw[ui] = %v", s.Raw["ui"])
	}
}

func TestResolverEnvOverridesFile(t *testing.T) {
	clearEnv(t)
	t.Setenv(ModelEnvVar, "anthropic:claude")
	t.Setenv(ServerEnvVar, "http://env:1")
	t.Setenv(ShellAllowListEnvVar, "all")
	s := resolve(t, writeConfig(t, `
models.default = "openai:gpt-5"
server.url = "http://file:1"
shell.allow_list = "ls"
`))
	if s.Model != "anthropic:claude" || s.ServerURL != "http://env:1" {
		t.Errorf("settings = %+v", s)
	}
	if !s.ShellAllowAll || s.ShellAllowList != nil {
		t.Errorf("allow all = %v, list = %v", s.ShellAllowAll, s.ShellAllowList)
	}
}

func TestResolverReadsBraveAPIKey(t *testing.T) {
	clearEnv(t)
	t.Setenv(BraveAPIKeyEnvVar, "brave-key")
	s := resolve(t, Paths{ConfigFile: filepath.Join(t.TempDir(), "absent.toml")})
	if s.WebSearchAPIKey != "brave-key" {
		t.Errorf("WebSearchAPIKey = %q", s.WebSearchAPIKey)
	}
}

func TestResolverReadsBraveAPIKeyFromDotEnv(t *testing.T) {
	clearEnv(t)
	if err := os.Unsetenv(BraveAPIKeyEnvVar); err != nil {
		t.Fatal(err)
	}
	dotEnv := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(dotEnv, []byte("BRAVE_API_KEY=from-dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := Paths{ConfigFile: filepath.Join(t.TempDir(), "absent.toml"), GlobalDotEnv: dotEnv}
	if err := LoadDotEnv(p, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if s := resolve(t, p); s.WebSearchAPIKey != "from-dotenv" {
		t.Errorf("WebSearchAPIKey = %q", s.WebSearchAPIKey)
	}
}

func TestResolverParseErrorReportsLine(t *testing.T) {
	clearEnv(t)
	_, err := NewResolver(writeConfig(t, "[models]\ndefault = \"x\"\nbroken = \n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v, want mention of line 3", err)
	}
}

func TestResolverRejectsBadValues(t *testing.T) {
	clearEnv(t)
	for name, body := range map[string]string{
		"mode":         `startup.mode = "sometimes"`,
		"auto mode":    `startup.mode = "auto"`,
		"retries":      `retries.max_retries = -1`,
		"allow type":   `shell.allow_list = 3`,
		"all combined": `shell.allow_list = ["all", "ls"]`,
	} {
		if _, err := NewResolver(writeConfig(t, body)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestParseShellAllowList(t *testing.T) {
	cmds, all, err := ParseShellAllowList(" recommended , make,ls ")
	if err != nil || all {
		t.Fatalf("err = %v, all = %v", err, all)
	}
	if !slices.Contains(cmds, "grep") || !slices.Contains(cmds, "make") {
		t.Errorf("cmds = %v", cmds)
	}
	if n := len(cmds); n != len(RecommendedSafeShellCommands)+1 {
		t.Errorf("len = %d, want recommended + make (ls deduped)", n)
	}
	if _, all, _ := ParseShellAllowList("ALL"); !all {
		t.Error(`"ALL" should allow everything`)
	}
	if cmds, all, _ := ParseShellAllowList(" , "); cmds != nil || all {
		t.Errorf("blank list = %v, %v", cmds, all)
	}
}

func TestResolverReadsWebSettings(t *testing.T) {
	clearEnv(t)
	t.Setenv(Context7APIKeyEnvVar, "ctx7sk-test")
	s := resolve(t, writeConfig(t, `
[models]
web = "anthropic:claude-haiku-4-5"

[storage]
results_ttl = "48h"
`))
	if s.WebModel != "anthropic:claude-haiku-4-5" || s.Context7APIKey != "ctx7sk-test" || s.ResultsTTL.Hours() != 48 {
		t.Errorf("settings = %+v", s)
	}
}

func TestResolverRejectsBadResultsTTL(t *testing.T) {
	clearEnv(t)
	for _, v := range []string{`"soon"`, `"-1h"`} {
		if _, err := NewResolver(writeConfig(t, "storage.results_ttl = "+v+"\n")); err == nil {
			t.Errorf("results_ttl = %s accepted", v)
		}
	}
}
