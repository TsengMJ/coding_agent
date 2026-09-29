package backend

// ContextHubClient is the subset of the LangSmith Hub API ContextHubBackend uses.
type ContextHubClient interface{}

// ContextHubBackend exposes a LangSmith Hub agent repo ("owner/name") as a
// file tree, queueing mutations and committing them back.
type ContextHubBackend struct {
	unimplemented
	identifier string
	client     ContextHubClient
}

// NewContextHubBackend returns a hub-backed file store.
func NewContextHubBackend(identifier string, client ContextHubClient) *ContextHubBackend {
	return &ContextHubBackend{unimplemented: unimplemented{name: "ContextHubBackend"}, identifier: identifier, client: client}
}
