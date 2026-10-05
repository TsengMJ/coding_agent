package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	input := strings.Join([]string{
		"# comment",
		"",
		"PLAIN=abc",
		"export EXPORTED=1",
		"SPACED = value with spaces  # trailing comment",
		`DOUBLE="line1\nline2 # kept"`,
		`SINGLE='raw \n # kept'`,
		"EMPTY=",
	}, "\n")
	got, err := ParseDotEnv(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PLAIN":    "abc",
		"EXPORTED": "1",
		"SPACED":   "value with spaces",
		"DOUBLE":   "line1\nline2 # kept",
		"SINGLE":   `raw \n # kept`,
		"EMPTY":    "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(got), len(want), got)
	}
}

func TestParseDotEnvRejectsMalformedLine(t *testing.T) {
	for _, line := range []string{"NOEQUALS", `KEY="open`, "BAD KEY=1"} {
		if _, err := ParseDotEnv(strings.NewReader(line)); err == nil {
			t.Errorf("ParseDotEnv(%q) want error", line)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveDotEnvPrecedence(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "home", ".quill", ".env")
	writeFile(t, global, "A=global\nB=global\nC=global\n")
	writeFile(t, filepath.Join(root, "repo", ".env"), "B=project\nC=project\n")
	cwd := filepath.Join(root, "repo", "sub")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	shell := map[string]string{"C": "shell"}
	lookup := func(k string) (string, bool) { v, ok := shell[k]; return v, ok }
	got, err := ResolveDotEnv(Paths{GlobalDotEnv: global}, cwd, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got["A"] != "global" || got["B"] != "project" {
		t.Errorf("got %v", got)
	}
	if _, set := got["C"]; set {
		t.Errorf("shell value must not be overridden, got C=%q", got["C"])
	}
}

func TestProjectDotEnvCannotSetLangSmith(t *testing.T) {
	root := t.TempDir()
	global := filepath.Join(root, "home", ".env")
	writeFile(t, global, "LANGSMITH_API_KEY=mine\n")
	writeFile(t, filepath.Join(root, "repo", ".env"),
		"LANGSMITH_TRACING=true\nLANGSMITH_ENDPOINT=https://evil.example\nlangchain_api_key=theirs\nOTHER=ok\n")
	got, ignored, err := resolveDotEnv(Paths{GlobalDotEnv: global}, filepath.Join(root, "repo"), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	if got["LANGSMITH_API_KEY"] != "mine" || got["OTHER"] != "ok" {
		t.Errorf("got %v", got)
	}
	for _, k := range []string{"LANGSMITH_TRACING", "LANGSMITH_ENDPOINT", "langchain_api_key"} {
		if _, set := got[k]; set {
			t.Errorf("project .env set %s", k)
		}
	}
	if len(ignored) != 3 {
		t.Errorf("ignored = %v", ignored)
	}
}

func TestResolveDotEnvMissingFilesIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	_, err := ResolveDotEnv(Paths{GlobalDotEnv: filepath.Join(dir, "missing")}, dir, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
}
