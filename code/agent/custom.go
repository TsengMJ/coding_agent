package agent

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	codesubagents "github.com/tsengmingchun/deepagents_golang/code/subagents"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
)

// builtinSubagents may not be redefined by a custom subagent.
var builtinSubagents = []string{subagents.GeneralPurposeName, ExplorerName, WebResearcherName}

// customSubagents turns the user's and the project's AGENTS.md subagents
// into specs. They inherit the main agent's tools, and its model unless
// they name one.
func customSubagents(opts Options) ([]subagents.Spec, error) {
	var userDir string
	if opts.UserDir != "" {
		name := opts.AgentName
		if name == "" {
			name = DefaultAgentName
		}
		userDir = codesubagents.UserDir(opts.UserDir, name)
	}
	found, err := codesubagents.List(userDir, codesubagents.ProjectDir(opts.Cwd))
	if err != nil {
		return nil, err
	}
	specs := make([]subagents.Spec, 0, len(found))
	for _, m := range found {
		if slices.Contains(builtinSubagents, m.Name) {
			return nil, fmt.Errorf("subagents: %s: %q is a built-in subagent; choose another name", m.Path, m.Name)
		}
		s := subagents.SubAgent{Name: m.Name, Description: m.Description, SystemPrompt: m.SystemPrompt, Model: opts.Model}
		if m.Model != "" {
			model, err := llm.InitChatModel(m.Model, modelOptions(opts.Settings))
			if err != nil {
				return nil, fmt.Errorf("subagents: %s: model %q: %w", m.Path, m.Model, err)
			}
			s.Model = trace.WrapModel(model)
		}
		specs = append(specs, s)
	}
	if opts.UserDir == "" {
		return specs, nil
	}
	async, err := codesubagents.LoadAsync(filepath.Join(opts.UserDir, config.ConfigFileName))
	if err != nil {
		return nil, err
	}
	for _, a := range async {
		if slices.Contains(builtinSubagents, a.Name) || slices.ContainsFunc(found, func(m codesubagents.Metadata) bool { return m.Name == a.Name }) {
			return nil, fmt.Errorf("subagents: [async_subagents.%s] reuses the name of another subagent; choose another name", a.Name)
		}
		specs = append(specs, a)
	}
	return specs, nil
}

func modelOptions(s *config.Settings) map[string]any {
	if s == nil || s.MaxRetries == nil {
		return nil
	}
	return map[string]any{"max_retries": *s.MaxRetries}
}
