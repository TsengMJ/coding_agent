package agent

import "github.com/tsengmingchun/deepagents_golang/tool"

// ResponseFormat selects how structured output is produced
// (langchain `ToolStrategy` / `ProviderStrategy` / `AutoStrategy`).
type ResponseFormat interface {
	OutputSchema() tool.Schema
}

// ToolStrategy extracts structured output through a synthetic tool call,
// named after the schema's "title" (default DefaultStructuredToolName). A
// valid call becomes State.StructuredResponse and ends the run.
type ToolStrategy struct{ Schema tool.Schema }

// ProviderStrategy uses the provider's native structured-output mode; not
// implemented yet.
type ProviderStrategy struct{ Schema tool.Schema }

// AutoStrategy picks ProviderStrategy when supported, else ToolStrategy;
// for now it always uses ToolStrategy.
type AutoStrategy struct{ Schema tool.Schema }

func (s ToolStrategy) OutputSchema() tool.Schema     { return s.Schema }
func (s ProviderStrategy) OutputSchema() tool.Schema { return s.Schema }
func (s AutoStrategy) OutputSchema() tool.Schema     { return s.Schema }
