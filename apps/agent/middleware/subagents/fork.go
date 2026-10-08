package subagents

import (
	"slices"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/middleware/summarization"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// ForkedContextKey is set on a forked subagent's own state, never on the
// parent's, so its `task` tool refuses to delegate further.
const ForkedContextKey = "_quill_forked_context"

const forkRecursionRefusal = "You are a subagent and cannot delegate to another subagent. Complete this task yourself instead of calling this tool again."

// forkTaskPreamble marks the replayed history, including the request to
// delegate, as already handled, and points the fork at the facts in it.
const forkTaskPreamble = "[The messages above are a prior conversation you are continuing as the " +
	"subagent that was just invoked. Any mention in them of delegating to a " +
	"subagent already happened — you are that subagent, not the one being " +
	"asked to delegate further. If you try to delegate to another subagent " +
	"yourself, it will be refused — complete this task directly. Use the " +
	"specific facts, figures, and identifiers already established in that " +
	"conversation when completing the task below — do not answer " +
	"generically when exact details are already available above. Your " +
	"actual task is below.]\n\n"

// forkedToolNote follows a fork's line in the task listing; without it the
// "stateless" usage note is the model's only signal about the subagent.
const forkedToolNote = " (inherits your full conversation and system prompt — no need to restate context here)"

// forkExcludedStateKeys are not resumed by a declarative fork: the
// summarization event is folded into its messages, its session gets a new
// ID, and a prior structured response must not pass for its result.
var forkExcludedStateKeys = []string{"messages", "structured_response", summarization.EventStateKey, summarization.SessionIDStateKey}

func isFork(spec Spec) bool {
	switch s := spec.(type) {
	case SubAgent:
		return s.Mode == ModeFork
	case CompiledSubAgent:
		return s.Mode == ModeFork
	}
	return false
}

// input is what a subagent starts from: the task alone, or for a fork the
// parent's effective conversation followed by the task.
func (m *Middleware) input(spec Spec, description string, parent *runtime.State) agent.Input {
	var values map[string]any
	if parent != nil {
		values = parent.Values
	}
	if !isFork(spec) {
		return agent.Input{Messages: []llm.Message{llm.NewUserMessage(description)}, Values: m.filterValues(values)}
	}
	in := agent.Input{Messages: forkMessages(parent, description)}
	if _, declarative := spec.(SubAgent); !declarative {
		// A compiled runnable is opaque; it gets only the shareable state.
		in.Values = m.filterValues(values)
		return in
	}
	// A declarative fork has the parent's middleware shape, so it resumes
	// the parent's private state too.
	in.Values = map[string]any{ForkedContextKey: true}
	for k, v := range values {
		if !slices.Contains(forkExcludedStateKeys, k) {
			in.Values[k] = v
		}
	}
	return in
}

// forkMessages drops the parent's pending tool-call message, applies its
// summarization event and appends the task.
func forkMessages(parent *runtime.State, description string) []llm.Message {
	var history []llm.Message
	if parent != nil {
		history = slices.Clone(parent.Messages)
	}
	if n := len(history); n > 0 && history[n-1].Role == llm.RoleAssistant && len(history[n-1].ToolCalls) > 0 {
		history = history[:n-1]
	}
	out := summarization.EffectiveMessages(history, parent)
	return append(out, llm.NewUserMessage(forkTaskPreamble+description))
}

// forkTaskTool gives a declarative fork the parent's `task` tool, so both
// send the same tools; the forked-context state makes it refuse at call time.
type forkTaskTool struct{ task tool.Tool }

func (forkTaskTool) Name() string { return "_ForkTaskToolMiddleware" }

func (f forkTaskTool) Tools() []tool.Tool { return []tool.Tool{f.task} }

func (forkTaskTool) StateSchema() runtime.Schema {
	return runtime.Schema{ForkedContextKey: {Name: ForkedContextKey, Private: true}}
}

// withForkTaskTool puts the task tool right after the filesystem tools, its
// place in the parent's tool list.
func withForkTaskTool(mw []agent.Middleware, task tool.Tool) []agent.Middleware {
	i := slices.IndexFunc(mw, func(m agent.Middleware) bool { return m.Name() == "FilesystemMiddleware" })
	return slices.Insert(slices.Clone(mw), i+1, agent.Middleware(forkTaskTool{task: task}))
}
