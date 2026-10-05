package agent

import (
	"fmt"
	"strings"

	"github.com/tsengmingchun/quill/middleware/subagents"
)

// subagentsSection tells the main agent when to delegate with `task` and
// when not to, naming only the subagents this configuration has.
func subagentsSection(opts Options) string {
	var b strings.Builder
	b.WriteString("## Subagents\n\n" +
		"The `task` tool runs a subagent in its own context window; only its final report comes back to you. " +
		"Delegating keeps long searches, file dumps and web pages out of your context, and independent pieces of work run in parallel.\n\n" +
		"Delegate when:\n")
	fmt.Fprintf(&b, "- You need to find where something lives, trace how a feature works across files, or read many files to answer a question: use `%s` (read-only). Ask for file paths, line numbers and the exact facts you need.\n", ExplorerName)
	if hasWebResearcher(opts) {
		fmt.Fprintf(&b, "- A question needs more than one web search or page: use `%s`.\n", WebResearcherName)
	}
	fmt.Fprintf(&b, "- The work splits into independent parts (separate modules, separate questions): start one `task` per part in a single message so they run in parallel.\n"+
		"- A self-contained multi-step job whose intermediate output you do not need to see: use `%s`.\n\n", subagents.GeneralPurposeName)
	b.WriteString("Do it yourself when:\n" +
		"- You already know the file or symbol: read it directly.\n" +
		"- The change is small or touches one or two files.\n" +
		"- You need the intermediate results to decide your next step.\n\n" +
		"A subagent cannot see this conversation. Write each `description` as a complete brief: the goal, what you already know, the paths or versions involved, and exactly what the report must contain. " +
		"Do not repeat a delegated search yourself while it runs; work on something else or wait for the report.\n")
	return b.String()
}
