package agent

import (
	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/langsmith"
)

// DefaultLangSmithProject receives dcode's runs unless tracing.project or
// $LANGSMITH_PROJECT names another.
const DefaultLangSmithProject = "dcode"

// NewLangSmith returns the LangSmith client when tracing.langsmith or
// LANGSMITH_TRACING=true turns tracing on, nil when it is off, and an error
// when it is on without an API key. Close the client before exiting.
func NewLangSmith(s *config.Settings) (*langsmith.Client, error) {
	on := langsmith.TracingEnabled()
	project := ""
	if s != nil {
		on = on || s.LangSmithTracing
		project = s.LangSmithProject
	}
	if !on {
		return nil, nil
	}
	cfg := langsmith.ConfigFromEnv()
	switch {
	case project != "":
		cfg.Project = project
	case cfg.Project == "":
		cfg.Project = DefaultLangSmithProject
	}
	return langsmith.New(cfg)
}
