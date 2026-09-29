// Package runtime provides the execution substrate that LangGraph supplies in
// Python: graph state with reducers, checkpoints, a key-value store,
// interrupts, run configuration, and streamed events.
package runtime

import (
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// RemoveAllMessages is a message ID that, when present in an update, clears
// the history before the remaining update messages are applied
// (LangGraph `REMOVE_ALL_MESSAGES`).
const RemoveAllMessages = "__remove_all__"

// State is the agent graph state (`AgentState` / `DeepAgentState`).
type State struct {
	Messages []llm.Message
	// StructuredResponse holds the parsed structured output, if any.
	StructuredResponse any
	// Values holds extension fields contributed by middleware or a custom
	// schema (e.g. "files", "todos", "skills_metadata", "memory_contents").
	Values map[string]any
}

// Update is a partial state change returned by nodes, hooks and tools.
type Update struct {
	Messages           []llm.Message
	StructuredResponse any
	Values             map[string]any
}

// Reducer merges an update value into the current value of one state key.
type Reducer func(current, update any) (any, error)

// KeySpec declares one state field (a middleware `state_schema` entry).
type KeySpec struct {
	Name string
	// Reducer merges updates; nil means last-write-wins.
	Reducer Reducer
	// Private keys are not propagated into subagents (`PrivateStateAttr`).
	Private bool
	// Input / Output limit visibility in the graph input / output schemas.
	OmitFromInput  bool
	OmitFromOutput bool
}

// Schema is the full set of declared state keys, indexed by name.
type Schema map[string]KeySpec

// Apply merges u into s using the reducers declared in schema.
func (s *State) Apply(u Update, schema Schema) error {
	return notimpl.Err("runtime.State.Apply")
}

// Clone returns a deep copy suitable for handing to a subagent.
func (s *State) Clone() *State {
	return nil
}

// MessagesReducer appends messages, replacing ones with matching IDs and
// honoring RemoveAllMessages (LangGraph `add_messages`).
func MessagesReducer(current, update []llm.Message) ([]llm.Message, error) {
	return nil, notimpl.Err("runtime.MessagesReducer")
}

// MergeSchemas combines schemas from a custom state schema and middleware.
func MergeSchemas(schemas ...Schema) (Schema, error) {
	return nil, notimpl.Err("runtime.MergeSchemas")
}

// PrivateKeys returns the names of keys marked Private across schemas.
func PrivateKeys(schemas ...Schema) []string {
	return nil
}
