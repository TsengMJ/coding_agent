package agent

import (
	"context"
	"iter"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// Config describes an agent (the arguments of langchain `create_agent`).
type Config struct {
	Model          llm.ChatModel
	SystemMessage  *llm.Message
	Tools          []tool.Tool
	Middleware     []Middleware
	ResponseFormat ResponseFormat
	// StateSchema is the base schema, merged with middleware schemas.
	StateSchema  runtime.Schema
	Checkpointer runtime.Checkpointer
	Store        runtime.Store
	Name         string
	Debug        bool
	// RecursionLimit caps loop steps; zero means runtime.DefaultRecursionLimit.
	RecursionLimit int
	Metadata       map[string]any
}

// Input starts or resumes a run.
type Input struct {
	Messages []llm.Message
	// Values seeds extension state keys (e.g. "files" for StateBackend).
	Values map[string]any
	// Resume continues a thread paused on an InterruptError.
	Resume *runtime.Command
}

// Agent is a runnable agent graph (`CompiledStateGraph`).
type Agent interface {
	Name() string
	Invoke(ctx context.Context, in Input, cfg runtime.RunConfig) (*runtime.State, error)
	Stream(ctx context.Context, in Input, cfg runtime.RunConfig, modes ...runtime.StreamMode) iter.Seq2[runtime.Event, error]
	GetState(ctx context.Context, threadID string) (*runtime.State, error)
}

// CreateAgent builds the model/tool loop with the given middleware stack.
func CreateAgent(cfg Config) (Agent, error) {
	return newLoopAgent(cfg)
}
