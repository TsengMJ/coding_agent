package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
)

// ErrRecursionLimit is returned when a run exceeds its model-call budget
// (LangGraph `GraphRecursionError`).
var ErrRecursionLimit = errors.New("agent: recursion limit reached")

// GotoModel is the AfterAgent Command target that re-enters the loop.
const GotoModel = "model"

type loopAgent struct {
	cfg        Config
	tools      []tool.Tool
	toolByName map[string]tool.Tool
	schema     runtime.Schema
	modelChain ModelHandler
	toolChain  ToolHandler
}

func newLoopAgent(cfg Config) (*loopAgent, error) {
	if cfg.Model == nil {
		return nil, errors.New("agent.CreateAgent: Model is required")
	}
	if cfg.Checkpointer != nil {
		return nil, notimpl.Err("agent.CreateAgent(Checkpointer)")
	}
	if err := checkMiddlewareNames(cfg.Middleware); err != nil {
		return nil, err
	}
	tools, byName, err := collectTools(cfg)
	if err != nil {
		return nil, err
	}
	a := &loopAgent{cfg: cfg, tools: tools, toolByName: byName, schema: collectSchema(cfg)}
	a.modelChain = chainModel(cfg.Middleware, a.callModel)
	a.toolChain = chainTool(cfg.Middleware, a.callTool)
	return a, nil
}

func checkMiddlewareNames(mws []Middleware) error {
	seen := make(map[string]bool, len(mws))
	for _, m := range mws {
		if m == nil {
			return errors.New("agent.CreateAgent: nil middleware")
		}
		if seen[m.Name()] {
			return fmt.Errorf("agent.CreateAgent: duplicate middleware %q", m.Name())
		}
		seen[m.Name()] = true
	}
	return nil
}

func collectTools(cfg Config) ([]tool.Tool, map[string]tool.Tool, error) {
	all := slices.Clone(cfg.Tools)
	for _, m := range cfg.Middleware {
		if p, ok := m.(ToolProvider); ok {
			all = append(all, p.Tools()...)
		}
	}
	byName := make(map[string]tool.Tool, len(all))
	for _, t := range all {
		if _, dup := byName[t.Name()]; dup {
			return nil, nil, fmt.Errorf("agent.CreateAgent: duplicate tool %q", t.Name())
		}
		byName[t.Name()] = t
	}
	return all, byName, nil
}

func collectSchema(cfg Config) runtime.Schema {
	schema := runtime.Schema{}
	for k, v := range cfg.StateSchema {
		schema[k] = v
	}
	for _, m := range cfg.Middleware {
		if p, ok := m.(StateSchemaProvider); ok {
			for k, v := range p.StateSchema() {
				schema[k] = v
			}
		}
	}
	return schema
}

func (a *loopAgent) Name() string { return a.cfg.Name }

func (a *loopAgent) Invoke(ctx context.Context, in Input, cfg runtime.RunConfig) (*runtime.State, error) {
	if in.Resume != nil {
		return nil, notimpl.Err("agent.Invoke(Resume)")
	}
	rt := &Runtime{Config: cfg, Store: a.cfg.Store}
	state := &runtime.State{Messages: slices.Clone(in.Messages)}
	for k, v := range in.Values {
		if err := state.Apply(runtime.Update{Values: map[string]any{k: v}}, a.schema); err != nil {
			return nil, err
		}
	}

	if err := a.runBeforeAgent(ctx, state, rt); err != nil {
		return nil, err
	}
	limit := a.recursionLimit(cfg)
	for step := 0; ; step++ {
		if step >= limit {
			return nil, fmt.Errorf("%w (%d steps)", ErrRecursionLimit, limit)
		}
		done, err := a.step(ctx, state, rt)
		if err != nil {
			return nil, err
		}
		if done {
			break
		}
	}
	if err := a.runAfterAgent(ctx, state, rt); err != nil {
		return nil, err
	}
	return state, nil
}

// step runs one model call and any tool calls it requested. It reports true
// when the model produced no tool calls.
func (a *loopAgent) step(ctx context.Context, state *runtime.State, rt *Runtime) (bool, error) {
	for _, m := range a.cfg.Middleware {
		if h, ok := m.(BeforeModelHook); ok {
			if err := a.applyHook(state, m, func() (*runtime.Update, error) { return h.BeforeModel(ctx, state, rt) }); err != nil {
				return false, err
			}
		}
	}

	resp, err := a.modelChain(ctx, a.buildModelRequest(state, rt))
	if err != nil {
		return false, err
	}
	if err := state.Apply(runtime.Update{Messages: resp.Messages, StructuredResponse: resp.StructuredResponse}, a.schema); err != nil {
		return false, err
	}
	if resp.Update != nil {
		if err := state.Apply(*resp.Update, a.schema); err != nil {
			return false, err
		}
	}

	for i := len(a.cfg.Middleware) - 1; i >= 0; i-- {
		m := a.cfg.Middleware[i]
		if h, ok := m.(AfterModelHook); ok {
			if err := a.applyHook(state, m, func() (*runtime.Update, error) { return h.AfterModel(ctx, state, rt) }); err != nil {
				return false, err
			}
		}
	}

	calls := pendingToolCalls(state)
	if len(calls) == 0 {
		return true, nil
	}
	for _, call := range calls {
		if err := a.runTool(ctx, state, rt, call); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (a *loopAgent) runBeforeAgent(ctx context.Context, state *runtime.State, rt *Runtime) error {
	for _, m := range a.cfg.Middleware {
		if h, ok := m.(BeforeAgentHook); ok {
			if err := a.applyHook(state, m, func() (*runtime.Update, error) { return h.BeforeAgent(ctx, state, rt) }); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *loopAgent) runAfterAgent(ctx context.Context, state *runtime.State, rt *Runtime) error {
	for i := len(a.cfg.Middleware) - 1; i >= 0; i-- {
		m := a.cfg.Middleware[i]
		h, ok := m.(AfterAgentHook)
		if !ok {
			continue
		}
		cmd, err := h.AfterAgent(ctx, state, rt)
		if err != nil {
			return fmt.Errorf("middleware %q AfterAgent: %w", m.Name(), err)
		}
		if cmd == nil {
			continue
		}
		if cmd.Goto == GotoModel {
			return notimpl.Err("agent.AfterAgent(Goto model)")
		}
		if cmd.Update != nil {
			if err := state.Apply(*cmd.Update, a.schema); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *loopAgent) applyHook(state *runtime.State, m Middleware, hook func() (*runtime.Update, error)) error {
	u, err := hook()
	if err != nil {
		return fmt.Errorf("middleware %q: %w", m.Name(), err)
	}
	if u == nil {
		return nil
	}
	return state.Apply(*u, a.schema)
}

func (a *loopAgent) recursionLimit(cfg runtime.RunConfig) int {
	if cfg.RecursionLimit > 0 {
		return cfg.RecursionLimit
	}
	if a.cfg.RecursionLimit > 0 {
		return a.cfg.RecursionLimit
	}
	return runtime.DefaultRecursionLimit
}

func (a *loopAgent) buildModelRequest(state *runtime.State, rt *Runtime) *ModelRequest {
	return &ModelRequest{
		Model:          a.cfg.Model,
		SystemMessage:  a.cfg.SystemMessage,
		Messages:       slices.Clone(state.Messages),
		Tools:          slices.Clone(a.tools),
		ResponseFormat: a.cfg.ResponseFormat,
		State:          state,
		Runtime:        rt,
	}
}

// callModel is the innermost model handler.
func (a *loopAgent) callModel(ctx context.Context, req *ModelRequest) (*ModelResponse, error) {
	msgs := make([]llm.Message, 0, len(req.Messages)+1)
	if req.SystemMessage != nil {
		msgs = append(msgs, *req.SystemMessage)
	}
	msgs = append(msgs, req.Messages...)
	specs := make([]llm.ToolSpec, len(req.Tools))
	for i, t := range req.Tools {
		specs[i] = tool.Spec(t)
	}
	reply, err := req.Model.Generate(ctx, llm.ChatRequest{
		Messages:   msgs,
		Tools:      specs,
		ToolChoice: req.ToolChoice,
		Settings:   req.ModelSettings,
	})
	if err != nil {
		return nil, fmt.Errorf("model call: %w", err)
	}
	return &ModelResponse{Messages: []llm.Message{*reply}}, nil
}

// pendingToolCalls returns the tool calls of the last message when it is an
// assistant message.
func pendingToolCalls(state *runtime.State) []llm.ToolCall {
	if len(state.Messages) == 0 {
		return nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last.Role != llm.RoleAssistant {
		return nil
	}
	return last.ToolCalls
}

func (a *loopAgent) runTool(ctx context.Context, state *runtime.State, rt *Runtime, call llm.ToolCall) error {
	res, err := a.toolChain(ctx, &ToolCallRequest{
		ToolCall: call,
		Tool:     a.toolByName[call.Name],
		State:    state,
		Runtime:  rt,
	})
	if err != nil {
		return err
	}
	if res.Command != nil {
		if res.Command.Goto != "" {
			return notimpl.Err("agent.tool(Command.Goto)")
		}
		if res.Command.Update != nil {
			return state.Apply(*res.Command.Update, a.schema)
		}
	}
	return state.Apply(runtime.Update{Messages: []llm.Message{toolMessage(call, res)}}, a.schema)
}

// callTool is the innermost tool handler. Unknown tools and tool failures
// become error results so the model can correct itself.
func (a *loopAgent) callTool(ctx context.Context, req *ToolCallRequest) (*tool.Result, error) {
	if req.Tool == nil {
		return errorResult(fmt.Sprintf("Error: %s is not a valid tool, try one of [%s].",
			req.ToolCall.Name, strings.Join(a.toolNames(), ", "))), nil
	}
	res, err := req.Tool.Invoke(ctx, &tool.Runtime{
		ToolCallID: req.ToolCall.ID,
		State:      req.State,
		Store:      req.Runtime.Store,
		Config:     req.Runtime.Config,
		Context:    req.Runtime.Context,
		Emit:       req.Runtime.Emit,
	}, req.ToolCall.Args)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return errorResult(fmt.Sprintf("Error: %v", err)), nil
	}
	if res == nil {
		return &tool.Result{}, nil
	}
	return res, nil
}

func (a *loopAgent) toolNames() []string {
	names := make([]string, len(a.tools))
	for i, t := range a.tools {
		names[i] = t.Name()
	}
	return names
}

func errorResult(text string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(text)}, IsError: true}
}

func toolMessage(call llm.ToolCall, res *tool.Result) llm.Message {
	msg := llm.Message{
		Role:       llm.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
		Status:     llm.ToolStatusSuccess,
		Content:    res.Content,
	}
	if res.IsError {
		msg.Status = llm.ToolStatusError
	}
	if res.Artifact != nil {
		msg.Metadata = map[string]any{"artifact": res.Artifact}
	}
	return msg
}

func (a *loopAgent) Stream(ctx context.Context, in Input, cfg runtime.RunConfig, modes ...runtime.StreamMode) iter.Seq2[runtime.Event, error] {
	return func(yield func(runtime.Event, error) bool) {
		yield(runtime.Event{}, notimpl.Err("agent.Stream"))
	}
}

func (a *loopAgent) GetState(ctx context.Context, threadID string) (*runtime.State, error) {
	return nil, notimpl.Err("agent.GetState")
}
