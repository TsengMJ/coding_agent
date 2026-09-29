// Package subagents loads subagent definitions from markdown files with
// frontmatter (Python `deepagents_code/subagents.py`).
package subagents

import (
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/middleware/asyncsubagents"
)

// Metadata is one parsed subagent file.
type Metadata struct {
	Name         string
	Description  string
	SystemPrompt string
	Model        string
	Path         string
	Source       string
}

// List loads subagents from user and project directories (project wins).
func List(userDir, projectDir string) ([]Metadata, error) {
	return nil, notimpl.Err("code/subagents.List")
}

// LoadAsync reads async subagent definitions from config.toml.
func LoadAsync(configPath string) ([]asyncsubagents.AsyncSubAgent, error) {
	return nil, notimpl.Err("code/subagents.LoadAsync")
}
