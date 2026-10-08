// Package rubric grades the agent's final answer against a rubric with a
// separate grader model and sends it back for revision until satisfied
// (Python `RubricMiddleware`).
package rubric

import (
	"context"

	"github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/internal/notimpl"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// GraderMessageSource tags messages injected by the grader.
const GraderMessageSource = "rubric_grader"

// Result is the grading outcome.
type Result string

const (
	ResultSatisfied            Result = "satisfied"
	ResultNeedsRevision        Result = "needs_revision"
	ResultMaxIterationsReached Result = "max_iterations_reached"
	ResultFailed               Result = "failed"
	ResultGraderError          Result = "grader_error"
)

// State keys ("rubric" is public input; the rest are private).
const (
	RubricStateKey      = "rubric"
	StatusStateKey      = "_rubric_status"
	IterationsStateKey  = "_rubric_iterations"
	EvaluationsStateKey = "_rubric_evaluations"
)

// CriterionEval is one graded criterion; Gap explains a failure.
type CriterionEval struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Gap    string `json:"gap,omitempty"`
}

// GraderResponse is the grader's structured output.
type GraderResponse struct {
	Result      Result          `json:"result"`
	Explanation string          `json:"explanation"`
	Criteria    []CriterionEval `json:"criteria"`
}

// Evaluation records one grading round.
type Evaluation struct {
	GradingRunID string
	Iteration    int
	Result       Result
	Explanation  string
	Criteria     []CriterionEval
	Unverified   bool
}

// Options configures the middleware.
type Options struct {
	Model            llm.ChatModel
	SystemPrompt     string
	Tools            []tool.Tool
	GraderMiddleware []agent.Middleware
	// MaxIterations defaults to 3.
	MaxIterations int
	OnEvaluation  func(Evaluation)
	// PrepareMessages filters the transcript sent to the grader.
	PrepareMessages func([]llm.Message) []llm.Message
}

// Middleware runs the grade/revise loop in AfterAgent.
type Middleware struct {
	opts Options
}

// New validates options and returns the middleware.
func New(opts Options) (*Middleware, error) {
	return &Middleware{opts: opts}, nil
}

func (m *Middleware) Name() string { return "RubricMiddleware" }

func (m *Middleware) StateSchema() runtime.Schema {
	return runtime.Schema{
		RubricStateKey:      {Name: RubricStateKey},
		StatusStateKey:      {Name: StatusStateKey, Private: true},
		IterationsStateKey:  {Name: IterationsStateKey, Private: true},
		EvaluationsStateKey: {Name: EvaluationsStateKey, Private: true},
	}
}

func (m *Middleware) BeforeAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	return nil, notimpl.Err("rubric.Middleware.BeforeAgent")
}

func (m *Middleware) AfterAgent(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Command, error) {
	return nil, notimpl.Err("rubric.Middleware.AfterAgent")
}
