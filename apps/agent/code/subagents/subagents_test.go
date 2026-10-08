package subagents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, dir, folder, content string) string {
	t.Helper()
	path := filepath.Join(dir, folder, FileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestListParsesDefinition(t *testing.T) {
	dir := t.TempDir()
	path := writeAgent(t, dir, "researcher", "---\ndescription: Researches things\nmodel: openai:gpt-x\n---\n\nYou research.\n")
	got, err := List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Metadata{Name: "researcher", Description: "Researches things", SystemPrompt: "You research.", Model: "openai:gpt-x", Path: path, Source: SourceUser}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("List = %+v", got)
	}
}

func TestListUsesFrontmatterName(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "folder", "---\nname: reviewer\ndescription: Reviews\n---\nReview.")
	got, err := List("", dir)
	if err != nil || len(got) != 1 || got[0].Name != "reviewer" || got[0].Source != SourceProject {
		t.Fatalf("List = %+v, %v", got, err)
	}
}

func TestListProjectOverridesUser(t *testing.T) {
	user, project := t.TempDir(), t.TempDir()
	writeAgent(t, user, "a", "---\ndescription: from user\n---\nU")
	writeAgent(t, user, "b", "---\ndescription: only user\n---\nB")
	writeAgent(t, project, "a", "---\ndescription: from project\n---\nP")
	got, err := List(user, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Description != "from project" || got[1].Name != "b" {
		t.Fatalf("List = %+v", got)
	}
}

func TestListMissingDirectoriesAreEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "none"), "")
	if err != nil || len(got) != 0 {
		t.Fatalf("List = %+v, %v", got, err)
	}
}

func TestListRejectsMalformedDefinitions(t *testing.T) {
	cases := map[string]string{
		"no description": "---\nname: x\n---\nbody",
		"empty name":     "---\nname: \"  \"\ndescription: d\n---\nbody",
		"model not text": "---\ndescription: d\nmodel: [a]\n---\nbody",
		"no frontmatter": "just a body",
		"bad yaml":       "---\ndescription: [unclosed\n---\nbody",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeAgent(t, dir, "x", content)
			if _, err := List(dir, ""); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v, want one naming %s", err, path)
			}
		})
	}
}

func TestListRejectsMisplacedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("---\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(dir, ""); err == nil || !strings.Contains(err.Error(), "<name>/AGENTS.md") {
		t.Fatalf("stray file: err = %v", err)
	}
	dir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x", "agent.md"), []byte("---\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := List(dir, ""); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("misnamed file: err = %v", err)
	}
}

func TestLoadAsyncReadsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[models]\ndefault = \"x\"\n\n[async_subagents.researcher]\ndescription = \"Research agent\"\nurl = \"https://r.example\"\ngraph_id = \"agent\"\nheaders = { \"x-api-key\" = \"k\" }\n\n[async_subagents.coder]\ndescription = \"Coder\"\nurl = \"https://c.example\"\ngraph_id = \"code\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAsync(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "coder" || got[1].Name != "researcher" || got[1].GraphID != "agent" || got[1].Headers["x-api-key"] != "k" {
		t.Fatalf("LoadAsync = %+v", got)
	}
}

func TestLoadAsyncMissingFileOrSection(t *testing.T) {
	dir := t.TempDir()
	if got, err := LoadAsync(filepath.Join(dir, "none.toml")); err != nil || len(got) != 0 {
		t.Fatalf("missing file: %v, %v", got, err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[models]\ndefault = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := LoadAsync(path); err != nil || len(got) != 0 {
		t.Fatalf("no section: %v, %v", got, err)
	}
}

func TestLoadAsyncRejectsIncompleteEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[async_subagents.r]\ndescription = \"d\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAsync(path); err == nil || !strings.Contains(err.Error(), "graph_id, url") || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
}

func TestListRejectsDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "a", "---\nname: same\ndescription: d\n---\n")
	writeAgent(t, dir, "b", "---\nname: same\ndescription: d\n---\n")
	if _, err := List(dir, ""); err == nil || !strings.Contains(err.Error(), "both define") {
		t.Fatalf("err = %v", err)
	}
}
