// Package server runs the coding agent for clients, either in-process or
// behind `dcode serve` (Python `server_graph.py`, `client/launch/server*.py`).
package server

import (
	"context"
	"errors"
	"iter"
	"os"

	"github.com/tsengmingchun/deepagents_golang/code/config"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
)

// DefaultModelSpec is used when neither Config.ModelSpec nor DCODE_MODEL is set.
const DefaultModelSpec = "openai:gpt-5.5"

// ModelEnvVar overrides the default model spec.
const ModelEnvVar = config.ModelEnvVar

var (
	// ErrBadRequest wraps malformed run requests.
	ErrBadRequest = errors.New("bad run request")
	// ErrThreadBusy is returned when a thread already has a run in flight.
	ErrThreadBusy = errors.New("thread is busy with another run")
	// ErrThreadCwdMismatch is returned when a thread is reused from another directory.
	ErrThreadCwdMismatch = errors.New("thread belongs to a different working directory")
	// ErrPendingApproval is returned when new messages arrive while tool calls await review.
	ErrPendingApproval = errors.New("thread has tool calls awaiting approval; resume it with decisions or start a new thread")
	// ErrNoPendingApproval is returned when a resume arrives without a pending review.
	ErrNoPendingApproval = errors.New("thread has no tool calls awaiting approval")
	// ErrThreadNotFound is returned when an operation needs an existing thread.
	ErrThreadNotFound = errors.New("thread not found")
	// ErrNothingToCompact is returned when a thread is too short to compact.
	ErrNothingToCompact = errors.New("conversation is too short to compact")
)

// CompactResult describes a summarization done by Compact.
type CompactResult struct {
	ThreadID string `json:"thread_id"`
	// Summary is the message that stands in for the older history.
	Summary string `json:"summary"`
	// FilePath is where the full older history was saved, relative to the
	// thread's cwd; empty when saving failed.
	FilePath string `json:"file_path,omitempty"`
}

// RunRequest starts a run on a thread, or resumes one paused for approval.
// Exactly one of Messages and Resume must be set.
type RunRequest struct {
	ThreadID string        `json:"thread_id"`
	Cwd      string        `json:"cwd"`
	Messages []llm.Message `json:"messages,omitempty"`
	Resume   *Resume       `json:"resume,omitempty"`
	// AutoApprove skips human review of tool calls.
	AutoApprove bool `json:"auto_approve,omitempty"`
	// Interactive selects the system prompt for a human-attended session.
	Interactive bool `json:"interactive,omitempty"`
	// Model ("provider:model") runs this request on another model; empty
	// uses the model the thread last ran with, else the server default.
	Model string `json:"model,omitempty"`
}

// Resume answers the pending review of a thread, one decision per action request.
type Resume struct {
	Decisions []hitl.Decision `json:"decisions"`
}

// EventKind names a run event; the values double as the NDJSON "event" field.
type EventKind string

const (
	EventDelta     EventKind = "delta"
	EventMessage   EventKind = "message"
	EventInterrupt EventKind = "interrupt"
	EventDone      EventKind = "done"
	EventError     EventKind = "error"
	// EventTrace reports a model call or file change just recorded.
	EventTrace EventKind = "trace"
)

// Event is one item of a run stream.
type Event struct {
	Kind EventKind `json:"event"`
	// Text is a streamed fragment of assistant text (EventDelta).
	Text string `json:"text,omitempty"`
	// Message is a complete message added by this run (EventMessage).
	Message *llm.Message `json:"message,omitempty"`
	// Interrupts are tool calls awaiting review; the run has stopped (EventInterrupt).
	Interrupts []Interrupt `json:"interrupts,omitempty"`
	// Trace is the record behind an EventTrace.
	Trace *trace.Update `json:"trace,omitempty"`
	// Error is set on EventError (HTTP transport only).
	Error string `json:"error,omitempty"`
	// Namespace is set when the event comes from a nested subagent run, e.g.
	// ["task:<tool call id>"]; empty for the main agent.
	Namespace []string `json:"namespace,omitempty"`
}

// Interrupt is one pending review.
type Interrupt struct {
	ID             string               `json:"id"`
	ActionRequests []hitl.ActionRequest `json:"action_requests"`
}

// ThreadState is the latest persisted state of a thread.
type ThreadState struct {
	ThreadID   string        `json:"thread_id"`
	Messages   []llm.Message `json:"messages"`
	Interrupts []Interrupt   `json:"interrupts,omitempty"`
	// Model is the spec the thread runs with (its last model, else the default).
	Model string `json:"model,omitempty"`
}

// Server is what clients talk to, in-process (Service) or over HTTP.
type Server interface {
	// Run streams events for one run. Stopping iteration or cancelling ctx
	// cancels the run.
	Run(ctx context.Context, req RunRequest) iter.Seq2[Event, error]
	// GetThread returns nil when the thread has no state.
	GetThread(ctx context.Context, threadID string) (*ThreadState, error)
	ListThreads(ctx context.Context, f sessions.Filter) ([]sessions.Info, error)
	// Compact summarizes older messages of an idle thread so later runs
	// send the model a shorter history.
	Compact(ctx context.Context, threadID string) (*CompactResult, error)
	// GetTrace returns the recorded model calls, file diffs and subagent
	// messages of a thread; an unknown thread has an empty trace.
	GetTrace(ctx context.Context, threadID string) (*trace.Trace, error)
	// GetLLMCall returns one recorded model call with its full request;
	// the error wraps trace.ErrNotFound when there is none.
	GetLLMCall(ctx context.Context, threadID string, seq int) (*trace.LLMCallDetail, error)
	Close() error
}

// resolveModelSpec picks the model: Config.ModelSpec (flag), then
// DCODE_MODEL, then models.default from config.toml, then DefaultModelSpec.
func resolveModelSpec(cfg Config) string {
	if cfg.ModelSpec != "" {
		return cfg.ModelSpec
	}
	if spec := os.Getenv(ModelEnvVar); spec != "" {
		return spec
	}
	if cfg.Settings != nil && cfg.Settings.Model != "" {
		return cfg.Settings.Model
	}
	return DefaultModelSpec
}

// modelOptions returns provider settings derived from config.toml.
func modelOptions(cfg Config) map[string]any {
	if cfg.Settings == nil || cfg.Settings.MaxRetries == nil {
		return nil
	}
	return map[string]any{"max_retries": *cfg.Settings.MaxRetries}
}
