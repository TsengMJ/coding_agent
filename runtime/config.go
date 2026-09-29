package runtime

// DefaultRecursionLimit matches the limit create_deep_agent applies via `with_config`.
const DefaultRecursionLimit = 9_999

// RunConfig configures a single run (LangChain `RunnableConfig`).
type RunConfig struct {
	ThreadID       string
	RecursionLimit int
	Tags           []string
	Metadata       map[string]any
	// Configurable holds arbitrary run-scoped settings.
	Configurable map[string]any
}
