package agent

import "github.com/tsengmingchun/deepagents_golang/tool"

// ResponseFormat selects how structured output is produced
// (langchain `ToolStrategy` / `ProviderStrategy` / `AutoStrategy`).
type ResponseFormat interface {
	OutputSchema() tool.Schema
}

// ToolStrategy extracts structured output through a synthetic tool call.
type ToolStrategy struct{ Schema tool.Schema }

// ProviderStrategy uses the provider's native structured-output mode.
type ProviderStrategy struct{ Schema tool.Schema }

// AutoStrategy picks ProviderStrategy when supported, else ToolStrategy.
type AutoStrategy struct{ Schema tool.Schema }

func (s ToolStrategy) OutputSchema() tool.Schema     { return s.Schema }
func (s ProviderStrategy) OutputSchema() tool.Schema { return s.Schema }
func (s AutoStrategy) OutputSchema() tool.Schema     { return s.Schema }
