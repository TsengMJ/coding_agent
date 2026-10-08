// Package runtime provides graph state, checkpoints, a key-value store,
// interrupts, run configuration, and streamed events.
//
// Message merge and checkpoint storage delegate to langgraphgo.
// The cross-thread Store and the files reducer stay in this package.
package runtime

import (
	"fmt"
	"maps"
	"slices"

	"github.com/smallnest/langgraphgo/graph"

	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

// RemoveAllMessages is a message ID that, when present in an update, clears
// the history before the remaining update messages are applied
// (LangGraph `REMOVE_ALL_MESSAGES`).
const RemoveAllMessages = "__remove_all__"

// State is the agent graph state (`AgentState`).
type State struct {
	Messages []llm.Message `json:"messages,omitempty"`
	// StructuredResponse holds the parsed structured output, if any.
	StructuredResponse any `json:"structured_response,omitempty"`
	// Values holds extension fields contributed by middleware or a custom
	// schema (e.g. "files", "todos", "skills_metadata", "memory_contents").
	Values map[string]any `json:"values,omitempty"`
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
	if len(u.Messages) > 0 {
		msgs, err := MessagesReducer(s.Messages, u.Messages)
		if err != nil {
			return err
		}
		s.Messages = msgs
	}
	if u.StructuredResponse != nil {
		s.StructuredResponse = u.StructuredResponse
	}
	for key, value := range u.Values {
		if err := s.applyValue(key, value, schema); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) applyValue(key string, value any, schema Schema) error {
	if s.Values == nil {
		s.Values = map[string]any{}
	}
	spec, ok := schema[key]
	if !ok || spec.Reducer == nil {
		s.Values[key] = value
		return nil
	}
	merged, err := spec.Reducer(s.Values[key], value)
	if err != nil {
		return fmt.Errorf("reduce state key %q: %w", key, err)
	}
	s.Values[key] = merged
	return nil
}

// Clone copies the message slice and the Values map so the clone can be
// modified at the top level without affecting s. Values themselves are shared.
func (s *State) Clone() *State {
	out := &State{
		Messages:           slices.Clone(s.Messages),
		StructuredResponse: s.StructuredResponse,
	}
	if s.Values != nil {
		out.Values = maps.Clone(s.Values)
	}
	return out
}

// MessagesReducer appends messages, replacing ones with matching IDs and
// honoring RemoveAllMessages (LangGraph `add_messages`).
//
// ID merge is langgraphgo's AddMessages. RemoveAllMessages is applied first
// because that library reducer does not know the marker: messages after the
// last marker replace the whole history.
func MessagesReducer(current, update []llm.Message) ([]llm.Message, error) {
	if i := lastRemoveAll(update); i >= 0 {
		current = nil
		update = slices.Clone(update[i+1:])
	}
	if current == nil {
		current = []llm.Message{}
	}
	if len(update) == 0 {
		return slices.Clone(current), nil
	}
	merged, err := graph.AddMessages(current, update)
	if err != nil {
		return nil, err
	}
	msgs, ok := merged.([]llm.Message)
	if !ok {
		return nil, fmt.Errorf("add_messages: got %T", merged)
	}
	return msgs, nil
}

func lastRemoveAll(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].ID == RemoveAllMessages {
			return i
		}
	}
	return -1
}

// MergeSchemas combines schemas from a custom state schema and middleware.
func MergeSchemas(schemas ...Schema) (Schema, error) {
	return nil, notimpl.Err("runtime.MergeSchemas")
}

// PrivateKeys returns the names of keys marked Private across schemas.
func PrivateKeys(schemas ...Schema) []string {
	var keys []string
	for _, s := range schemas {
		for name, spec := range s {
			if spec.Private && !slices.Contains(keys, name) {
				keys = append(keys, name)
			}
		}
	}
	slices.Sort(keys)
	return keys
}
