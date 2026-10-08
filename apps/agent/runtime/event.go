package runtime

// StreamMode selects which events a stream emits (LangGraph `stream_mode`).
type StreamMode string

const (
	StreamValues   StreamMode = "values"
	StreamUpdates  StreamMode = "updates"
	StreamMessages StreamMode = "messages"
	StreamCustom   StreamMode = "custom"
)

// Event is one streamed item from a run.
type Event struct {
	Mode StreamMode
	// Node names the step that produced the event ("model", "tools", a middleware hook).
	Node string
	// Namespace identifies nested subagent runs; empty for the root agent.
	Namespace []string
	Data      any
}

// Emitter lets tools and middleware push custom events (LangGraph `get_stream_writer`).
type Emitter func(Event)
