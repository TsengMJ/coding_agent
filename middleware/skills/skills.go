// Package skills discovers SKILL.md files in backend directories and lists
// them in the system prompt for progressive disclosure (Python `SkillsMiddleware`).
package skills

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// Limits from the Agent Skills spec.
const (
	MaxSkillFileSize            = 10 * 1024 * 1024
	MaxSkillNameLength          = 64
	MaxSkillDescriptionLength   = 1024
	MaxSkillCompatibilityLength = 500
	MaxSkillsLoadWarnings       = 20
)

// State keys.
const (
	MetadataStateKey   = "skills_metadata"
	LoadErrorsStateKey = "skills_load_errors"
)

// Metadata is parsed SKILL.md frontmatter (`SkillMetadata`).
type Metadata struct {
	Path          string            `json:"path"`
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
}

// Source is a skill directory; Label defaults to one derived from Path.
// Later sources override earlier ones for skills with the same name.
type Source struct {
	Path  string
	Label string
}

// Options configures the middleware.
type Options struct {
	Backend      backend.Backend
	Sources      []Source
	SystemPrompt string
}

// Middleware loads skill metadata once per thread and injects the skill list.
type Middleware struct {
	opts Options
}

// New returns a skills middleware.
func New(opts Options) *Middleware { return &Middleware{opts: opts} }

// SourcesFromPaths converts bare paths into Sources.
func SourcesFromPaths(paths []string) []Source {
	out := make([]Source, len(paths))
	for i, p := range paths {
		out[i] = Source{Path: p}
	}
	return out
}

func (m *Middleware) Name() string { return "SkillsMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{
		MetadataStateKey:   {Name: MetadataStateKey, OmitFromOutput: true},
		LoadErrorsStateKey: {Name: LoadErrorsStateKey, Private: true},
	}
}

func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	return nil, notimpl.Err("skills.Middleware.BeforeAgent")
}

func (m *Middleware) WrapModelCall(ctx context.Context, req *agent.ModelRequest, next agent.ModelHandler) (*agent.ModelResponse, error) {
	return nil, notimpl.Err("skills.Middleware.WrapModelCall")
}

// ParseMetadata parses and validates SKILL.md content found at path.
func ParseMetadata(content, path, directoryName string) (*Metadata, error) {
	return nil, notimpl.Err("skills.ParseMetadata")
}

// ListSkills loads all skills under one source directory.
func ListSkills(ctx context.Context, b backend.Backend, sourcePath string) ([]Metadata, error) {
	return nil, notimpl.Err("skills.ListSkills")
}
