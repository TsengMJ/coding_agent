package agent

import (
	"fmt"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/summarization"
)

// HistoryPathPrefix is where summarized history is offloaded, relative to
// the project root, so it stays out of the way but readable by file tools.
const HistoryPathPrefix = "/.deepagents/conversation_history"

// NewBackend is the default project backend: a LocalShellBackend rooted at
// cwd in virtual mode.
func NewBackend(cwd string) backend.Backend {
	return backend.NewLocalShellBackend(backend.LocalShellOptions{
		FilesystemOptions: backend.FilesystemOptions{RootDir: cwd, VirtualMode: true, RespectGitignore: true},
		InheritEnv:        true,
	})
}

// NewSummarization builds the CLI's summarization middleware; it also
// serves /compact.
func NewSummarization(model llm.ChatModel, b backend.Backend) (*summarization.Middleware, error) {
	opts, err := summarization.DefaultOptions(model, b)
	if err != nil {
		return nil, fmt.Errorf("code/agent: summarization: %w", err)
	}
	opts.HistoryPathPrefix = HistoryPathPrefix
	return summarization.New(opts)
}
