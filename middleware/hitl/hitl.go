// Package hitl pauses tool calls for human approval
// (langchain `HumanInTheLoopMiddleware`).
package hitl

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

// AutoApproveKey is the RunConfig.Configurable key that, when true, lets
// every tool call through without review.
const AutoApproveKey = "hitl_auto_approve"

// DecisionType is a reviewer's response to an interrupted tool call.
type DecisionType string

const (
	DecisionApprove DecisionType = "approve"
	DecisionEdit    DecisionType = "edit"
	DecisionReject  DecisionType = "reject"
	DecisionRespond DecisionType = "respond"
)

// defaultAllowed applies when InterruptOnConfig.AllowedDecisions is empty.
var defaultAllowed = []DecisionType{DecisionApprove, DecisionReject}

// InterruptOnConfig configures review for one tool (`InterruptOnConfig`).
type InterruptOnConfig struct {
	AllowedDecisions []DecisionType
	Description      string
	// When restricts the interrupt to matching calls; nil means always.
	When func(call llm.ToolCall) bool
}

// ActionRequest describes one tool call awaiting review (the interrupt value).
type ActionRequest struct {
	ToolCallID       string         `json:"tool_call_id"`
	Name             string         `json:"name"`
	Args             map[string]any `json:"args"`
	Description      string         `json:"description,omitempty"`
	AllowedDecisions []DecisionType `json:"allowed_decisions,omitempty"`
}

// Decision is the resume value supplied by a reviewer for one ActionRequest.
type Decision struct {
	Type       DecisionType   `json:"type"`
	EditedArgs map[string]any `json:"edited_args,omitempty"`
	Message    string         `json:"message,omitempty"`
}

// Middleware interrupts after the model proposes calls to configured tools.
type Middleware struct {
	interruptOn map[string]InterruptOnConfig
}

// New returns a HITL middleware for the given per-tool configs.
func New(interruptOn map[string]InterruptOnConfig) *Middleware {
	return &Middleware{interruptOn: interruptOn}
}

func (m *Middleware) Name() string { return "HumanInTheLoopMiddleware" }

// AfterModel pauses on calls that need review, then applies the decisions:
// approved calls run, rejected calls get an error tool message instead.
func (m *Middleware) AfterModel(ctx context.Context, state *runtime.State, rt *agent.Runtime) (*runtime.Update, error) {
	if len(m.interruptOn) == 0 || len(state.Messages) == 0 {
		return nil, nil
	}
	if rt != nil {
		if auto, _ := rt.Config.Configurable[AutoApproveKey].(bool); auto {
			return nil, nil
		}
	}
	last := state.Messages[len(state.Messages)-1]
	if last.Role != llm.RoleAssistant {
		return nil, nil
	}
	var reqs []ActionRequest
	for _, call := range last.ToolCalls {
		cfg, ok := m.interruptOn[call.Name]
		if !ok || (cfg.When != nil && !cfg.When(call)) {
			continue
		}
		reqs = append(reqs, ActionRequest{
			ToolCallID:       call.ID,
			Name:             call.Name,
			Args:             call.Args,
			Description:      describe(cfg, call),
			AllowedDecisions: allowed(cfg),
		})
	}
	if len(reqs) == 0 {
		return nil, nil
	}
	v, err := agent.Interrupt(ctx, reqs)
	if err != nil {
		return nil, err
	}
	decisions, err := DecodeDecisions(v)
	if err != nil {
		return nil, err
	}
	if len(decisions) != len(reqs) {
		return nil, fmt.Errorf("hitl: got %d decisions for %d pending tool calls", len(decisions), len(reqs))
	}
	var msgs []llm.Message
	for i, d := range decisions {
		req := reqs[i]
		if !slices.Contains(req.AllowedDecisions, d.Type) {
			return nil, fmt.Errorf("hitl: decision %q is not allowed for %s (allowed: %v)", d.Type, req.Name, req.AllowedDecisions)
		}
		switch d.Type {
		case DecisionApprove:
		case DecisionReject:
			text := d.Message
			if text == "" {
				text = fmt.Sprintf("User rejected the tool call for `%s` with id %s", req.Name, req.ToolCallID)
			}
			msg := llm.NewToolMessage(req.ToolCallID, req.Name, text)
			msg.Status = llm.ToolStatusError
			msgs = append(msgs, msg)
		default:
			return nil, fmt.Errorf("hitl: %q decisions are not supported yet", d.Type)
		}
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &runtime.Update{Messages: msgs}, nil
}

// DecodeDecisions accepts []Decision or its generic JSON form (after a
// checkpoint or HTTP round trip).
func DecodeDecisions(v any) ([]Decision, error) {
	if d, ok := v.([]Decision); ok {
		return d, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("hitl: encode resume value: %w", err)
	}
	var d []Decision
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("hitl: resume value is not a list of decisions: %w", err)
	}
	return d, nil
}

func describe(cfg InterruptOnConfig, call llm.ToolCall) string {
	if cfg.Description != "" {
		return cfg.Description
	}
	args, _ := json.Marshal(call.Args)
	return fmt.Sprintf("Tool execution requires approval\n\nTool: %s\nArgs: %s", call.Name, args)
}

func allowed(cfg InterruptOnConfig) []DecisionType {
	if len(cfg.AllowedDecisions) > 0 {
		return cfg.AllowedDecisions
	}
	return defaultAllowed
}

// Merge overlays user configs on generated ones; user entries win per tool.
// Returns nil when both are empty (`_merge_fs_interrupt_on`).
func Merge(generated, user map[string]InterruptOnConfig) map[string]InterruptOnConfig {
	if len(generated) == 0 && len(user) == 0 {
		return nil
	}
	out := maps.Clone(generated)
	if out == nil {
		out = map[string]InterruptOnConfig{}
	}
	maps.Copy(out, user)
	return out
}
