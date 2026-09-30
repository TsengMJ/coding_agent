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
	// Describe renders a per-call description and wins over Description
	// (Python's callable `description`).
	Describe func(call llm.ToolCall) string
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
// approved calls run, edited calls run with the reviewer's args (replacing
// them in the stored assistant message), rejected calls get an error tool
// message and responded calls get the reviewer's message as their result.
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
	edited := map[string]map[string]any{}
	for i, d := range decisions {
		req := reqs[i]
		if !slices.Contains(req.AllowedDecisions, d.Type) {
			return nil, fmt.Errorf("hitl: decision %q is not allowed for %s (allowed: %v)", d.Type, req.Name, req.AllowedDecisions)
		}
		switch d.Type {
		case DecisionApprove:
		case DecisionEdit:
			if d.EditedArgs == nil {
				return nil, fmt.Errorf("hitl: edit decision for %s (%s) has no edited_args", req.Name, req.ToolCallID)
			}
			edited[req.ToolCallID] = d.EditedArgs
		case DecisionReject:
			text := d.Message
			if text == "" {
				text = fmt.Sprintf("User rejected the tool call for `%s` with id %s", req.Name, req.ToolCallID)
			}
			msg := llm.NewToolMessage(req.ToolCallID, req.Name, text)
			msg.Status = llm.ToolStatusError
			msgs = append(msgs, msg)
		case DecisionRespond:
			msgs = append(msgs, llm.NewToolMessage(req.ToolCallID, req.Name, d.Message))
		default:
			return nil, fmt.Errorf("hitl: unknown decision type %q for %s", d.Type, req.Name)
		}
	}
	if len(edited) > 0 {
		revised, err := withEditedArgs(last, edited)
		if err != nil {
			return nil, err
		}
		msgs = append([]llm.Message{revised}, msgs...)
	}
	if len(msgs) == 0 {
		return nil, nil
	}
	return &runtime.Update{Messages: msgs}, nil
}

// withEditedArgs copies ai with the reviewer's args swapped in; the copy
// keeps ai's ID so the messages reducer replaces the original in place.
func withEditedArgs(ai llm.Message, edited map[string]map[string]any) (llm.Message, error) {
	if ai.ID == "" {
		return llm.Message{}, fmt.Errorf("hitl: cannot apply edits to an assistant message without an ID")
	}
	ai.ToolCalls = slices.Clone(ai.ToolCalls)
	for i, call := range ai.ToolCalls {
		if args, ok := edited[call.ID]; ok {
			ai.ToolCalls[i].Args = args
		}
	}
	return ai, nil
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
	if cfg.Describe != nil {
		return cfg.Describe(call)
	}
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
