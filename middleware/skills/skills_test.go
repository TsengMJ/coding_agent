package skills

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func skillMD(name, description string, extra ...string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n" + strings.Join(extra, "\n") + "\n---\n\n# Body\n"
}

func TestParseMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		dir     string
		wantErr string
		check   func(*testing.T, *Metadata)
	}{
		{name: "full", dir: "web-research",
			content: skillMD("web-research", "Research the web.", "license: MIT", "compatibility: needs curl",
				"allowed-tools: web_search, fetch_url  read_file", "metadata:\n  author: me\n  version: 2"),
			check: func(t *testing.T, m *Metadata) {
				if m.Name != "web-research" || m.Description != "Research the web." || m.License != "MIT" || m.Compatibility != "needs curl" {
					t.Errorf("metadata = %+v", m)
				}
				if !slices.Equal(m.AllowedTools, []string{"web_search", "fetch_url", "read_file"}) {
					t.Errorf("allowed tools = %v", m.AllowedTools)
				}
				if m.Metadata["author"] != "me" || m.Metadata["version"] != "2" || m.Path != "/s/web-research/SKILL.md" {
					t.Errorf("metadata = %+v", m)
				}
			}},
		{name: "allowed tools list", dir: "x", content: skillMD("x", "d", "allowed-tools:\n  - a\n  - ' b '\n  - 3"),
			check: func(t *testing.T, m *Metadata) {
				if !slices.Equal(m.AllowedTools, []string{"a", "b"}) {
					t.Errorf("allowed tools = %v", m.AllowedTools)
				}
			}},
		{name: "name mismatch still loads", dir: "other", content: skillMD("my-skill", "d"),
			check: func(t *testing.T, m *Metadata) {
				if m.Name != "my-skill" {
					t.Errorf("name = %q", m.Name)
				}
			}},
		{name: "long description truncated", dir: "x", content: skillMD("x", strings.Repeat("a", 1100)),
			check: func(t *testing.T, m *Metadata) {
				if len(m.Description) != MaxSkillDescriptionLength {
					t.Errorf("description length = %d", len(m.Description))
				}
			}},
		{name: "no frontmatter", dir: "x", content: "# Just markdown\n", wantErr: "no valid YAML frontmatter"},
		{name: "missing name", dir: "x", content: "---\ndescription: d\n---\n", wantErr: "missing required 'name'"},
		{name: "missing description", dir: "x", content: "---\nname: x\n---\n", wantErr: "missing required"},
		{name: "invalid yaml", dir: "x", content: "---\nname: [x\n---\n", wantErr: "invalid YAML"},
		{name: "not a mapping", dir: "x", content: "---\n- a\n- b\n---\n", wantErr: "invalid YAML"},
		{name: "too large", dir: "x", content: skillMD("x", "d") + strings.Repeat("x", MaxSkillFileSize), wantErr: "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := ParseMetadata(tc.content, "/s/"+tc.dir+"/SKILL.md", tc.dir)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, m)
		})
	}
}

func TestValidateName(t *testing.T) {
	for name, want := range map[string]string{
		"pdf-tools":                 "",
		"café":                      "",
		"":                          "required",
		strings.Repeat("a", 65):     "exceeds 64",
		"-lead":                     "single hyphens",
		"trail-":                    "single hyphens",
		"dou--ble":                  "single hyphens",
		"Upper":                     "single hyphens",
		"under_score":               "single hyphens",
		"mismatch":                  "must match directory",
		strings.Repeat("a", 64)[:0]: "required",
	} {
		dir := name
		if name == "mismatch" {
			dir = "other"
		}
		err := ValidateName(name, dir)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("ValidateName(%q) = %v, want %q", name, err, want)
		}
	}
}

func TestDeriveLabel(t *testing.T) {
	for path, want := range map[string]string{
		"/skills/user/":            "User",
		"/home/me/.claude/skills":  "Claude",
		"/x/my_team-tools/skills/": "My Team Tools",
		"/opt/built_in_skills":     "Built-in",
		"/skills":                  "Skills",
		"/":                        "Unnamed",
	} {
		if got := DeriveLabel(path); got != want {
			t.Errorf("DeriveLabel(%q) = %q, want %q", path, got, want)
		}
	}
}

// writeSkills creates <root>/<source>/<name>/SKILL.md files.
func writeSkills(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newFS(t *testing.T) (backend.Backend, string) {
	dir := t.TempDir()
	return backend.NewFilesystemBackend(backend.FilesystemOptions{RootDir: dir, VirtualMode: true}), dir
}

func TestListSkillsSkipsNonSkillsAndBrokenFiles(t *testing.T) {
	b, dir := newFS(t)
	writeSkills(t, dir, map[string]string{
		"skills/good/SKILL.md":      skillMD("good", "A good skill."),
		"skills/broken/SKILL.md":    "no frontmatter",
		"skills/no-skill/README.md": "not a skill",
		"skills/loose-file.md":      "ignored",
		"skills/second/SKILL.md":    skillMD("second", "Another."),
	})
	got, err := ListSkills(context.Background(), b, "/skills")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"good", "second"}) {
		t.Fatalf("skills = %v", names)
	}
	if got[0].Path != "/skills/good/SKILL.md" && got[1].Path != "/skills/good/SKILL.md" {
		t.Errorf("paths = %+v", got)
	}
}

func TestListSkillsReportsUnreadableSource(t *testing.T) {
	b, _ := newFS(t)
	_, err := ListSkills(context.Background(), b, "/missing")
	if err == nil || !strings.Contains(err.Error(), "Cannot load skills from '/missing'") {
		t.Fatalf("err = %v", err)
	}
}

// runAgent runs one model turn through the middleware and returns the final
// state and the system prompt the model saw.
func runAgent(t *testing.T, m *Middleware, in agent.Input, cp runtime.Checkpointer, threadID string) (*runtime.State, string) {
	t.Helper()
	model := llm.NewFakeModel(llm.NewAssistantMessage("ok"))
	a, err := agent.CreateAgent(agent.Config{Model: model, Middleware: []agent.Middleware{m}, Checkpointer: cp})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), in, runtime.RunConfig{ThreadID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	req := model.Requests()[0]
	if len(req.Messages) == 0 || req.Messages[0].Role != llm.RoleSystem {
		t.Fatalf("no system message: %+v", req.Messages)
	}
	return st, req.Messages[0].Text()
}

func userInput(text string) agent.Input {
	return agent.Input{Messages: []llm.Message{llm.NewUserMessage(text)}}
}

func TestLaterSourcesOverrideAndPromptListsSkills(t *testing.T) {
	b, dir := newFS(t)
	writeSkills(t, dir, map[string]string{
		"user/skills/shared/SKILL.md":    skillMD("shared", "User version."),
		"user/skills/only-user/SKILL.md": skillMD("only-user", "Only in user.", "license: MIT", "allowed-tools: read_file"),
		"proj/skills/shared/SKILL.md":    skillMD("shared", "Project version."),
	})
	m, err := New(Options{Backend: b, Sources: []Source{{Path: "/user/skills"}, {Path: "/proj/skills", Label: "Project"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, prompt := runAgent(t, m, userInput("hi"), nil, "")
	for _, want := range []string{
		"## Skills System",
		"**User Skills**: `/user/skills`\n**Project Skills**: `/proj/skills` (higher priority)",
		"- **shared**: Project version.\n  -> Read `/proj/skills/shared/SKILL.md` for full instructions",
		"- **only-user**: Only in user. (License: MIT)\n  -> Allowed tools: read_file",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "User version.") || strings.Contains(prompt, "skill_load_warnings") {
		t.Errorf("overridden skill or spurious warnings in prompt:\n%s", prompt)
	}
}

func TestEmptySourcesSuggestWhereToCreateSkills(t *testing.T) {
	b, dir := newFS(t)
	writeSkills(t, dir, map[string]string{"a/.keep": ""})
	m, _ := New(Options{Backend: b, Sources: SourcesFromPaths([]string{"/a"})})
	_, prompt := runAgent(t, m, userInput("hi"), nil, "")
	if !strings.Contains(prompt, "(No skills available yet. You can create skills in /a)") {
		t.Fatalf("prompt:\n%s", prompt)
	}
}

func TestLoadWarningsAreEscapedAndCapped(t *testing.T) {
	b, _ := newFS(t)
	var sources []string
	for i := range MaxSkillsLoadWarnings + 2 {
		sources = append(sources, "/missing<"+string(rune('a'+i))+">")
	}
	m, _ := New(Options{Backend: b, Sources: SourcesFromPaths(sources)})
	_, prompt := runAgent(t, m, userInput("hi"), nil, "")
	if !strings.Contains(prompt, "<skill_load_warnings>") || !strings.Contains(prompt, "untrusted diagnostics") {
		t.Fatalf("prompt:\n%s", prompt)
	}
	if strings.Count(prompt, "Cannot load skills from") != MaxSkillsLoadWarnings {
		t.Errorf("warnings shown = %d", strings.Count(prompt, "Cannot load skills from"))
	}
	if !strings.Contains(prompt, "2 additional skill loading warnings omitted.") {
		t.Error("omitted count missing")
	}
	warnings := prompt[strings.Index(prompt, "<skill_load_warnings>"):strings.Index(prompt, "</skill_load_warnings>")]
	if strings.Contains(warnings, "/missing<a>") || !strings.Contains(warnings, "/missing&lt;a&gt;") {
		t.Error("warnings must be HTML-escaped")
	}
}

func TestSkillsLoadOncePerThreadAndReloadOnNil(t *testing.T) {
	b, dir := newFS(t)
	writeSkills(t, dir, map[string]string{"s/first/SKILL.md": skillMD("first", "First.")})
	m, _ := New(Options{Backend: b, Sources: SourcesFromPaths([]string{"/s"})})
	cp := runtime.NewMemoryCheckpointer()
	runAgent(t, m, userInput("one"), cp, "t")
	writeSkills(t, dir, map[string]string{"s/second/SKILL.md": skillMD("second", "Second.")})
	_, prompt := runAgent(t, m, userInput("two"), cp, "t")
	if !strings.Contains(prompt, "**first**") || strings.Contains(prompt, "**second**") {
		t.Fatalf("second turn must reuse the cached skills:\n%s", prompt)
	}
	reload := userInput("three")
	reload.Values = map[string]any{MetadataStateKey: nil}
	_, prompt = runAgent(t, m, reload, cp, "t")
	if !strings.Contains(prompt, "**second**") {
		t.Fatalf("nil skills_metadata must trigger a reload:\n%s", prompt)
	}
}

func TestNewValidatesPromptSlots(t *testing.T) {
	b, _ := newFS(t)
	if _, err := New(Options{Backend: b, SystemPrompt: "no slots"}); err == nil || !strings.Contains(err.Error(), SlotList) {
		t.Fatalf("err = %v", err)
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("Backend is required")
	}
}
