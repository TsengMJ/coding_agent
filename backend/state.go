package backend

// FilesStateKey is the graph-state key holding StateBackend files
// (map[string]FileData; a nil value deletes the path).
const FilesStateKey = "files"

// StateBackend stores files in the agent graph state under FilesStateKey.
// Files are thread-scoped and checkpointed with the conversation.
// It requires a RunContext in ctx (see WithRunContext).
type StateBackend struct{ unimplemented }

// NewStateBackend returns a state-backed file store.
func NewStateBackend() *StateBackend {
	return &StateBackend{unimplemented{name: "StateBackend"}}
}
