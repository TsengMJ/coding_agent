// Package offload summarizes older messages on demand (/offload, /compact)
// and writes them to the backend (Python `offload*.py`).
package offload

import (
	"context"

	"github.com/tsengmingchun/deepagents_golang/backend"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// Result reports what an offload did.
type Result struct {
	MessagesOffloaded int
	TokensBefore      int
	TokensAfter       int
	FilePath          string
}

// Run compacts the thread state using model and stores history in b.
func Run(ctx context.Context, model llm.ChatModel, b backend.Backend, state *runtime.State) (*Result, *runtime.Update, error) {
	return nil, nil, notimpl.Err("offload.Run")
}
