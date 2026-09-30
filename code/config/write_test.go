package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setAndRead(t *testing.T, body *string, spec string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	if body != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(*body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := SetDefaultModel(path, spec); err != nil {
		t.Fatalf("SetDefaultModel: %v", err)
	}
	s := resolve(t, Paths{ConfigFile: path})
	if s.Model != spec {
		t.Fatalf("models.default = %q, want %q", s.Model, spec)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func ptr(s string) *string { return &s }

func TestSetDefaultModelCreatesFile(t *testing.T) {
	clearEnv(t)
	out := setAndRead(t, nil, "anthropic:claude-x")
	if !strings.Contains(out, "[models]") {
		t.Errorf("missing [models] table:\n%s", out)
	}
}

func TestSetDefaultModelReplacesInTableAndKeepsOtherKeys(t *testing.T) {
	clearEnv(t)
	body := "# my config\n[models]\ndefault = \"openai:old\" # pinned\n\n[shell]\nallow_list = [\"ls\"]\n"
	out := setAndRead(t, ptr(body), "openai:new")
	want := "# my config\n[models]\ndefault = \"openai:new\" # pinned\n\n[shell]\nallow_list = [\"ls\"]\n"
	if out != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestSetDefaultModelReplacesDottedKey(t *testing.T) {
	clearEnv(t)
	out := setAndRead(t, ptr("models.default = \"a:b\"\n[server]\nurl = \"http://x\"\n"), "c:d")
	if !strings.HasPrefix(out, "models.default = \"c:d\"\n") {
		t.Errorf("dotted key not replaced:\n%s", out)
	}
}

func TestSetDefaultModelInsertsIntoExistingTable(t *testing.T) {
	clearEnv(t)
	out := setAndRead(t, ptr("[models]\nother = 1\n[shell]\nallow_list = \"ls\"\n"), "c:d")
	if !strings.HasPrefix(out, "[models]\ndefault = \"c:d\"\nother = 1\n") {
		t.Errorf("not inserted after header:\n%s", out)
	}
}

func TestSetDefaultModelAppendsTable(t *testing.T) {
	clearEnv(t)
	out := setAndRead(t, ptr("[shell]\nallow_list = \"ls\""), "c:d")
	if !strings.HasSuffix(out, "[shell]\nallow_list = \"ls\"\n\n[models]\ndefault = \"c:d\"\n") {
		t.Errorf("table not appended:\n%s", out)
	}
}

func TestSetDefaultModelRejectsBrokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("broken = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultModel(path, "a:b"); err == nil {
		t.Fatal("want error for unparsable config")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "broken = \n" {
		t.Errorf("file changed: %q", got)
	}
}
