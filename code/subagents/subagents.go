// Package subagents loads subagent definitions from markdown files with
// frontmatter.
//
// A subagent lives at <dir>/<name>/AGENTS.md:
//
//	---
//	name: researcher   # optional; defaults to the folder name
//	description: Research topics before writing
//	model: anthropic:claude-haiku-4-5   # optional; defaults to the main model
//	---
//
//	You are a research assistant...
package subagents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/tsengmingchun/quill/middleware/asyncsubagents"
)

// FileName is the definition file inside a subagent's folder.
const FileName = "AGENTS.md"

// Sources of a loaded subagent.
const (
	SourceUser    = "user"
	SourceProject = "project"
)

// Metadata is one parsed subagent file.
type Metadata struct {
	Name         string
	Description  string
	SystemPrompt string
	// Model is a "provider:model" spec; empty uses the main model.
	Model  string
	Path   string
	Source string
}

// UserDir is the custom-subagent directory of an agent profile under
// ~/.quill.
func UserDir(userDir, agentName string) string {
	return filepath.Join(userDir, agentName, "agents")
}

// ProjectDir is the project's custom-subagent directory.
func ProjectDir(projectRoot string) string {
	return filepath.Join(projectRoot, ".quill", "agents")
}

// List loads subagents from user and project directories; a project
// subagent replaces a user one with the same name. Missing directories are
// skipped; a malformed definition is an error naming its file. The result
// is sorted by name.
func List(userDir, projectDir string) ([]Metadata, error) {
	all := map[string]Metadata{}
	for _, src := range []struct{ dir, source string }{{userDir, SourceUser}, {projectDir, SourceProject}} {
		if src.dir == "" {
			continue
		}
		found, err := loadDir(src.dir, src.source)
		if err != nil {
			return nil, err
		}
		for name, m := range found {
			all[name] = m
		}
	}
	out := make([]Metadata, 0, len(all))
	for _, m := range all {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadDir(dir, source string) (map[string]Metadata, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("subagents: %w", err)
	}
	found := map[string]Metadata{}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			if strings.EqualFold(filepath.Ext(e.Name()), ".md") {
				return nil, fmt.Errorf("subagents: %s: a subagent must be defined at %s/<name>/%s, not as a file in the agents directory", path, dir, FileName)
			}
			continue
		}
		file := filepath.Join(path, FileName)
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			if md, _ := filepath.Glob(filepath.Join(path, "*.md")); len(md) > 0 {
				return nil, fmt.Errorf("subagents: %s: expected %s but found %s; rename the definition to %s", path, FileName, filepath.Base(md[0]), FileName)
			}
			continue
		}
		m, err := parseFile(file, e.Name())
		if err != nil {
			return nil, err
		}
		m.Source = source
		if prev, ok := found[m.Name]; ok {
			return nil, fmt.Errorf("subagents: %s and %s both define subagent %q; give each a unique folder or frontmatter name", prev.Path, m.Path, m.Name)
		}
		found[m.Name] = m
	}
	return found, nil
}

var frontmatter = regexp.MustCompile(`(?s)\A---[ \t\r]*\n(.*?)\n---[ \t\r]*(?:\n|\z)(.*)\z`)

// parseFile reads one definition; folder is the name used when the
// frontmatter has none.
func parseFile(path, folder string) (Metadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, fmt.Errorf("subagents: %w", err)
	}
	match := frontmatter.FindStringSubmatch(string(raw))
	if match == nil {
		return Metadata{}, fmt.Errorf("subagents: %s: missing YAML frontmatter; start the file with a --- block containing at least description", path)
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(match[1]), &fm); err != nil {
		return Metadata{}, fmt.Errorf("subagents: %s: invalid YAML frontmatter: %w", path, err)
	}
	if fm == nil {
		return Metadata{}, fmt.Errorf("subagents: %s: frontmatter must be a mapping with a description", path)
	}
	var problems []string
	name := folder
	if v, ok := fm["name"]; ok {
		name = nonEmpty(v)
		if name == "" {
			problems = append(problems, "name (non-empty string required)")
		}
	}
	desc := nonEmpty(fm["description"])
	if desc == "" {
		problems = append(problems, "description (non-empty string required)")
	}
	model := ""
	if v, ok := fm["model"]; ok && v != nil {
		s, isString := v.(string)
		if !isString {
			problems = append(problems, "model (string required when present)")
		}
		model = strings.TrimSpace(s)
	}
	if len(problems) > 0 {
		return Metadata{}, fmt.Errorf("subagents: %s: invalid or missing frontmatter field(s): %s", path, strings.Join(problems, ", "))
	}
	return Metadata{Name: name, Description: desc, SystemPrompt: strings.TrimSpace(match[2]), Model: model, Path: path}, nil
}

func nonEmpty(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// asyncEntry is one [async_subagents.<name>] table.
type asyncEntry struct {
	Description string            `toml:"description"`
	URL         string            `toml:"url"`
	GraphID     string            `toml:"graph_id"`
	Headers     map[string]string `toml:"headers"`
}

// LoadAsync reads the remote agents of config.toml's [async_subagents]
// section, sorted by name:
//
//	[async_subagents.researcher]
//	description = "Research agent"
//	url = "https://my-deployment.langsmith.dev"
//	graph_id = "agent"
//	headers = { "x-api-key" = "..." }   # optional
//
// A missing file or section yields none; an incomplete entry is an error.
func LoadAsync(configPath string) ([]asyncsubagents.AsyncSubAgent, error) {
	var file struct {
		Async map[string]asyncEntry `toml:"async_subagents"`
	}
	if _, err := toml.DecodeFile(configPath, &file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("subagents: %s: %w", configPath, err)
	}
	names := make([]string, 0, len(file.Async))
	for name := range file.Async {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]asyncsubagents.AsyncSubAgent, 0, len(names))
	for _, name := range names {
		e := file.Async[name]
		var missing []string
		for field, v := range map[string]string{"description": e.Description, "url": e.URL, "graph_id": e.GraphID} {
			if strings.TrimSpace(v) == "" {
				missing = append(missing, field)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("subagents: %s: [async_subagents.%s] is missing %s", configPath, name, strings.Join(missing, ", "))
		}
		out = append(out, asyncsubagents.AsyncSubAgent{Name: name, Description: e.Description, URL: e.URL, GraphID: e.GraphID, Headers: e.Headers})
	}
	return out, nil
}
