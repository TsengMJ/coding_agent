package agent

import (
	"slices"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/codetools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// ExplorerName is the read-only subagent that searches the codebase.
const ExplorerName = "explorer"

const explorerDescription = "Read-only codebase explorer (ls, read_file, glob, grep, git_status, git_diff). " +
	"Use it proactively to find where something is defined or used, to map how a feature works across files, " +
	"or to answer a question that needs many files read, so those reads stay out of your context. " +
	"It cannot change files or run commands. Say what to find and what the report must contain."

const explorerPrompt = `You explore a codebase for a coding agent and report what you find. You can only read: you cannot change files or run commands.

Search broadly first (glob, grep), then read only the parts of files that matter. Run independent searches in parallel.

Your final message is all the agent sees. Make it a complete answer to the question you were given:
- The exact file paths and line numbers of the relevant code.
- The facts the agent asked for (names, signatures, values, call paths), quoted where precision matters.
- What you looked for but did not find, so the agent does not search again.

Keep it short; leave out files that turned out to be irrelevant.`

// explorerReadOnly are the tools the explorer keeps.
var explorerReadOnly = []string{"ls", "read_file", "glob", "grep", codetools.GitStatusName, codetools.GitDiffName}

// ReadOnlySubagents may run in plan mode because they cannot change anything.
var ReadOnlySubagents = []string{ExplorerName, WebResearcherName}

// explorer declares the read-only exploration subagent; codeTools supplies
// its git tools, the file tools come from the subagent's filesystem stack.
func explorer(model llm.ChatModel, codeTools []tool.Tool) subagents.SubAgent {
	git := slices.DeleteFunc(slices.Clone(codeTools), func(t tool.Tool) bool {
		return !slices.Contains(explorerReadOnly, t.Name())
	})
	return subagents.SubAgent{
		Name:         ExplorerName,
		Description:  explorerDescription,
		SystemPrompt: explorerPrompt,
		Model:        model,
		Tools:        git,
		ReadOnly:     true,
		Middleware: []core.Middleware{researchLimits{allowed: explorerReadOnly,
			refusal: "Error: `%s` is not available to the explorer, which only reads; report what you found instead."}},
	}
}
