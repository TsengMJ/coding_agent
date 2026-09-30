package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smallnest/langgraphgo/graph"

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

// nodeAfterModel runs AfterModel hooks in their own graph node so that an
// interrupt raised there resumes without repeating the model call.
const nodeAfterModel = "after_model"

// Interrupt pauses the run for human input (LangGraph `interrupt()`). Call it
// from AfterModel hooks before changing state: when the thread is resumed the
// hook runs again and Interrupt returns the resume value instead.
//
// A resume value is handed out once per run, so a later Interrupt in the same
// run pauses again.
func Interrupt(ctx context.Context, value any) (any, error) {
	if slot, ok := ctx.Value(resumeSlotKey{}).(*resumeSlot); ok {
		if v, ok := slot.take(); ok {
			return v, nil
		}
	}
	return nil, &graph.NodeInterrupt{Value: value}
}

type resumeSlotKey struct{}

type resumeSlot struct {
	value any
	used  bool
}

func (s *resumeSlot) take() (any, bool) {
	if s.used || s.value == nil {
		return nil, false
	}
	s.used = true
	return s.value, true
}

// execState is the langgraphgo graph state. Nodes return this pointer, or a
// *graph.Command whose Update is this pointer.
type execState struct {
	State *runtime.State
	Steps int
	Limit int
	RT    *Runtime
}

type loopAgent struct {
	cfg        Config
	tools      []tool.Tool
	toolByName map[string]tool.Tool
	schema     runtime.Schema
	modelChain ModelHandler
	toolChain  ToolHandler
	runnable   *graph.Runnable
}

func newLoopAgent(cfg Config) (*loopAgent, error) {
	if cfg.Model == nil {
		return nil, errors.New("agent.CreateAgent: Model is required")
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
	if err := a.compile(); err != nil {
		return nil, err
	}
	return a, nil
}

// compile builds the model/tool graph on langgraphgo's MessageGraph, which is
// the v0.5 runnable that applies Command, conditional edges, and GraphInterrupt.
// StateGraph.Compile in this version does not honor InterruptBefore.
func (a *loopAgent) compile() error {
	g := graph.NewMessageGraph()
	g.AddNode("before_agent", "before_agent", guardNode("before_agent", a.nodeBeforeAgent))
	g.AddNode(GotoModel, "model", guardNode(GotoModel, a.nodeModel))
	g.AddNode(nodeAfterModel, "after_model", guardNode(nodeAfterModel, a.nodeAfterModel))
	g.AddNode("tools", "tools", guardNode("tools", a.nodeTools))
	g.AddNode("after_agent", "after_agent", guardNode("after_agent", a.nodeAfterAgent))
	g.SetEntryPoint("before_agent")
	g.AddEdge("before_agent", GotoModel)
	g.AddEdge(GotoModel, nodeAfterModel)
	g.AddConditionalEdge(nodeAfterModel, func(_ context.Context, state any) string {
		st := state.(*execState)
		if len(pendingToolCalls(st.State)) > 0 {
			return "tools"
		}
		// Every call was answered in after_model (e.g. rejected in review):
		// let the model see the results.
		if n := len(st.State.Messages); n > 0 && st.State.Messages[n-1].Role == llm.RoleTool {
			return GotoModel
		}
		return "after_agent"
	})
	g.AddEdge("tools", GotoModel)
	g.AddEdge("after_agent", graph.END)
	runnable, err := g.Compile()
	if err != nil {
		return err
	}
	a.runnable = runnable
	return nil
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
	// A nested run (a subagent inside a streaming parent) must not stream
	// its model chunks into the parent's stream.
	ctx = context.WithValue(ctx, chunkEmitKey{}, nil)
	st, err := a.invoke(ctx, in, cfg, nil)
	if err != nil || st == nil {
		return nil, err
	}
	return st.State, nil
}

func (a *loopAgent) invoke(ctx context.Context, in Input, cfg runtime.RunConfig, emit func(runtime.Event) bool) (*execState, error) {
	st, resumeFrom, resumeValue, err := a.prepare(ctx, in, cfg)
	if err != nil {
		return nil, err
	}
	if emit != nil {
		st.RT.Emit = func(ev runtime.Event) { emit(ev) }
	}
	ctx = context.WithValue(ctx, resumeSlotKey{}, &resumeSlot{value: resumeValue})
	gcfg := &graph.Config{}
	if len(resumeFrom) > 0 {
		gcfg.ResumeFrom = resumeFrom
	} else if nodes, ok := cfg.Configurable["interrupt_before"].([]string); ok {
		gcfg.InterruptBefore = nodes
	}
	var saveErr error
	gcfg.Callbacks = []graph.CallbackHandler{&stepCallback{
		emit: emit,
		save: func(_ string, state *execState) {
			if err := a.save(ctx, cfg.ThreadID, state, nil); err != nil && saveErr == nil {
				saveErr = fmt.Errorf("save checkpoint: %w", err)
			}
		},
	}}

	out, err := a.runnable.InvokeWithConfig(ctx, st, gcfg)
	if err == nil && saveErr != nil {
		return nil, saveErr
	}
	var interrupted *graph.GraphInterrupt
	if errors.As(err, &interrupted) {
		paused := execFrom(interrupted.State)
		if paused == nil {
			paused = st
		}
		if saveErr := a.save(ctx, cfg.ThreadID, paused, interrupted); saveErr != nil {
			return nil, saveErr
		}
		return nil, &runtime.InterruptError{Interrupts: []runtime.Interrupt{{
			ID: interrupted.Node, Value: interrupted.InterruptValue,
		}}}
	}
	if err != nil {
		return nil, err
	}
	final := execFrom(out)
	if saveErr := a.save(ctx, cfg.ThreadID, final, nil); saveErr != nil {
		return nil, saveErr
	}
	return final, nil
}

// prepare builds the starting state. On resume it also returns the nodes to
// continue from and the resume value for the pending interrupt.
func (a *loopAgent) prepare(ctx context.Context, in Input, cfg runtime.RunConfig) (*execState, []string, any, error) {
	rt := &Runtime{Config: cfg, Store: a.cfg.Store}
	st := &execState{
		State: &runtime.State{Messages: slices.Clone(in.Messages)},
		Limit: a.recursionLimit(cfg),
		RT:    rt,
	}
	for k, v := range in.Values {
		if err := st.State.Apply(runtime.Update{Values: map[string]any{k: v}}, a.schema); err != nil {
			return nil, nil, nil, err
		}
	}
	if cfg.ThreadID == "" || a.cfg.Checkpointer == nil {
		if in.Resume != nil {
			return nil, nil, nil, errors.New("agent.Invoke: resume requires a checkpointer and thread id")
		}
		return st, nil, nil, nil
	}
	prev, err := a.cfg.Checkpointer.Latest(ctx, cfg.ThreadID)
	if err != nil {
		return nil, nil, nil, err
	}
	if in.Resume != nil {
		if prev == nil || len(prev.PendingInterrupts) == 0 {
			return nil, nil, nil, errors.New("agent.Invoke: no pending interrupt to resume")
		}
		st.State = prev.State.Clone()
		st.Steps = prev.Steps
		if in.Resume.Update != nil {
			if err := st.State.Apply(*in.Resume.Update, a.schema); err != nil {
				return nil, nil, nil, err
			}
		}
		return st, slices.Clone(prev.ResumeFrom), in.Resume.Resume[prev.PendingInterrupts[0].ID], nil
	}
	if prev != nil && len(prev.PendingInterrupts) == 0 {
		base := prev.State.Clone()
		if err := base.Apply(runtime.Update{Messages: in.Messages, Values: in.Values}, a.schema); err != nil {
			return nil, nil, nil, err
		}
		st.State = base
	}
	return st, nil, nil, nil
}

func (a *loopAgent) save(ctx context.Context, threadID string, st *execState, interrupted *graph.GraphInterrupt) error {
	if a.cfg.Checkpointer == nil || threadID == "" || st == nil || st.State == nil {
		return nil
	}
	cp := runtime.Checkpoint{
		ThreadID: threadID,
		State:    *st.State,
		Steps:    st.Steps,
	}
	if interrupted != nil {
		cp.PendingInterrupts = []runtime.Interrupt{{ID: interrupted.Node, Value: interrupted.InterruptValue}}
		nodes := interrupted.NextNodes
		if len(nodes) == 0 {
			nodes = []string{interrupted.Node}
		}
		cp.ResumeFrom = slices.Clone(nodes)
	}
	return a.cfg.Checkpointer.Put(ctx, cp)
}

func (a *loopAgent) nodeBeforeAgent(ctx context.Context, state any) (any, error) {
	st := state.(*execState)
	if err := a.runBeforeAgent(ctx, st.State, st.RT); err != nil {
		return nil, err
	}
	return st, nil
}

func (a *loopAgent) nodeModel(ctx context.Context, state any) (any, error) {
	st := state.(*execState)
	if st.Steps >= st.Limit {
		return nil, fmt.Errorf("%w (%d steps)", ErrRecursionLimit, st.Limit)
	}
	st.Steps++
	for _, m := range a.cfg.Middleware {
		if h, ok := m.(BeforeModelHook); ok {
			if err := a.applyHook(st.State, m, func() (*runtime.Update, error) { return h.BeforeModel(ctx, st.State, st.RT) }); err != nil {
				return nil, err
			}
		}
	}
	resp, err := a.modelChain(ctx, a.buildModelRequest(st.State, st.RT))
	if err != nil {
		return nil, err
	}
	if err := st.State.Apply(runtime.Update{Messages: resp.Messages, StructuredResponse: resp.StructuredResponse}, a.schema); err != nil {
		return nil, err
	}
	if resp.Update != nil {
		if err := st.State.Apply(*resp.Update, a.schema); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (a *loopAgent) nodeAfterModel(ctx context.Context, state any) (any, error) {
	st := state.(*execState)
	for i := len(a.cfg.Middleware) - 1; i >= 0; i-- {
		m := a.cfg.Middleware[i]
		if h, ok := m.(AfterModelHook); ok {
			if err := a.applyHook(st.State, m, func() (*runtime.Update, error) { return h.AfterModel(ctx, st.State, st.RT) }); err != nil {
				return nil, err
			}
		}
	}
	return st, nil
}

// maxParallelTools caps how many tool calls of one model turn run at once.
const maxParallelTools = 8

// nodeTools runs the pending tool calls concurrently against one state
// snapshot, then applies their results in call order so tool messages and
// Command updates stay deterministic.
func (a *loopAgent) nodeTools(ctx context.Context, state any) (any, error) {
	st := state.(*execState)
	calls := pendingToolCalls(st.State)
	snapshot := st.State.Clone()
	results := make([]*tool.Result, len(calls))
	timings := make([]llm.Timing, len(calls))
	errs := make([]error, len(calls))
	sem := make(chan struct{}, maxParallelTools)
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			defer func() { timings[i] = llm.Timing{Start: start, Duration: time.Since(start)} }()
			defer func() {
				if r := recover(); r != nil {
					errs[i] = panicError("tool "+call.Name, r)
				}
			}()
			results[i], errs[i] = a.toolChain(ctx, &ToolCallRequest{
				ToolCall: call,
				Tool:     a.toolByName[call.Name],
				State:    snapshot,
				Runtime:  st.RT,
			})
		}()
	}
	wg.Wait()
	for i, call := range calls {
		if errs[i] != nil {
			return nil, errs[i]
		}
		if err := a.applyToolResult(st.State, call, results[i], timings[i]); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func (a *loopAgent) nodeAfterAgent(ctx context.Context, state any) (any, error) {
	st := state.(*execState)
	for i := len(a.cfg.Middleware) - 1; i >= 0; i-- {
		m := a.cfg.Middleware[i]
		h, ok := m.(AfterAgentHook)
		if !ok {
			continue
		}
		cmd, err := h.AfterAgent(ctx, st.State, st.RT)
		if err != nil {
			return nil, fmt.Errorf("middleware %q AfterAgent: %w", m.Name(), err)
		}
		if cmd == nil {
			continue
		}
		if cmd.Update != nil {
			if err := st.State.Apply(*cmd.Update, a.schema); err != nil {
				return nil, err
			}
		}
		if cmd.Goto == GotoModel {
			return &graph.Command{Goto: GotoModel, Update: st}, nil
		}
	}
	return st, nil
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

// ErrPanic wraps a panic recovered inside a graph node or tool.
var ErrPanic = errors.New("panic")

// guardNode turns a panic in fn into an error. langgraphgo runs nodes in
// their own goroutines, where an unrecovered panic kills the process.
func guardNode(name string, fn func(context.Context, any) (any, error)) func(context.Context, any) (any, error) {
	return func(ctx context.Context, state any) (out any, err error) {
		defer func() {
			if r := recover(); r != nil {
				out, err = nil, panicError("node "+name, r)
			}
		}()
		return fn(ctx, state)
	}
}

func panicError(where string, r any) error {
	stack := strings.Split(string(debug.Stack()), "\n")
	if len(stack) > 16 {
		stack = stack[:16]
	}
	return fmt.Errorf("%w in %s: %v\n%s", ErrPanic, where, r, strings.Join(stack, "\n"))
}

func execFrom(state any) *execState {
	st, _ := state.(*execState)
	return st
}

// stepCallback forwards langgraphgo OnGraphStep events.
type stepCallback struct {
	graph.NoOpCallbackHandler
	emit func(runtime.Event) bool
	save func(node string, state *execState)
}

func (s stepCallback) OnGraphStep(_ context.Context, stepNode string, state any) {
	st := execFrom(state)
	if st == nil {
		return
	}
	node := strings.TrimPrefix(stepNode, "step:")
	if s.save != nil {
		s.save(node, st)
	}
	if s.emit == nil {
		return
	}
	s.emit(runtime.Event{Mode: runtime.StreamValues, Node: node, Data: st.State.Clone()})
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
	chatReq := llm.ChatRequest{
		Messages:   msgs,
		Tools:      specs,
		ToolChoice: req.ToolChoice,
		Settings:   req.ModelSettings,
	}
	var reply *llm.Message
	var err error
	start := time.Now()
	if emit, ok := ctx.Value(chunkEmitKey{}).(func(runtime.Event) bool); ok {
		reply, err = streamModel(ctx, req.Model, chatReq, emit)
	} else {
		reply, err = req.Model.Generate(ctx, chatReq)
	}
	if err != nil {
		return nil, fmt.Errorf("model call: %w", err)
	}
	timed := llm.WithTiming(*reply, llm.Timing{Start: start, Duration: time.Since(start)})
	return &ModelResponse{Messages: []llm.Message{timed}}, nil
}

// chunkEmitKey carries the Stream emitter when StreamMessages was requested.
type chunkEmitKey struct{}

// streamModel emits every non-final chunk as a StreamMessages event and
// returns the complete message from the Done chunk. Models without Stream
// fall back to Generate.
func streamModel(ctx context.Context, m llm.ChatModel, req llm.ChatRequest, emit func(runtime.Event) bool) (*llm.Message, error) {
	started := false
	for c, err := range m.Stream(ctx, req) {
		if err != nil {
			if !started && errors.Is(err, notimpl.ErrNotImplemented) {
				return m.Generate(ctx, req)
			}
			return nil, err
		}
		started = true
		if c.Done {
			reply := c.Delta
			if reply.Role == "" {
				reply.Role = llm.RoleAssistant
			}
			return &reply, nil
		}
		if !emit(runtime.Event{Mode: runtime.StreamMessages, Node: GotoModel, Data: c}) {
			return nil, context.Canceled
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, errors.New("model stream ended without a final message")
}

// pendingToolCalls returns the calls of the latest assistant message that no
// later tool message answers yet.
func pendingToolCalls(state *runtime.State) []llm.ToolCall {
	for i := len(state.Messages) - 1; i >= 0; i-- {
		m := state.Messages[i]
		if m.Role == llm.RoleTool {
			continue
		}
		if m.Role != llm.RoleAssistant {
			return nil
		}
		answered := map[string]bool{}
		for _, later := range state.Messages[i+1:] {
			answered[later.ToolCallID] = true
		}
		var out []llm.ToolCall
		for _, call := range m.ToolCalls {
			if !answered[call.ID] {
				out = append(out, call)
			}
		}
		return out
	}
	return nil
}

// applyToolResult applies one call's result and records its timing on the
// tool message answering it.
func (a *loopAgent) applyToolResult(state *runtime.State, call llm.ToolCall, res *tool.Result, t llm.Timing) error {
	if res == nil {
		res = &tool.Result{}
	}
	if res.Command != nil {
		if res.Command.Goto != "" {
			return notimpl.Err("agent.tool(Command.Goto)")
		}
		if res.Command.Update != nil {
			upd := *res.Command.Update
			upd.Messages = slices.Clone(upd.Messages)
			for i, m := range upd.Messages {
				if m.Role == llm.RoleTool && m.ToolCallID == call.ID {
					upd.Messages[i] = llm.WithTiming(m, t)
				}
			}
			return state.Apply(upd, a.schema)
		}
	}
	msg := llm.WithTiming(toolMessage(call, res), t)
	upd := runtime.Update{Messages: []llm.Message{msg}}
	if res.Update != nil {
		upd.Messages = append(slices.Clone(res.Update.Messages), msg)
		upd.StructuredResponse = res.Update.StructuredResponse
		upd.Values = res.Update.Values
	}
	return state.Apply(upd, a.schema)
}

// callTool is the innermost tool handler. Unknown tools, tool failures, and
// tool panics become error results so the model can correct itself.
func (a *loopAgent) callTool(ctx context.Context, req *ToolCallRequest) (res *tool.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = errorResult(fmt.Sprintf("Error: tool %s panicked: %v", req.ToolCall.Name, r)), nil
		}
	}()
	if req.Tool == nil {
		return errorResult(fmt.Sprintf("Error: %s is not a valid tool, try one of [%s].",
			req.ToolCall.Name, strings.Join(a.toolNames(), ", "))), nil
	}
	res, err = req.Tool.Invoke(tool.WithCallID(ctx, req.ToolCall.ID), &tool.Runtime{
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
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		// Tools running in parallel (e.g. several subagents) emit
		// concurrently, and yield must not be called concurrently.
		var mu sync.Mutex
		stopped := false
		emit := func(ev runtime.Event) bool {
			mu.Lock()
			defer mu.Unlock()
			if stopped {
				return false
			}
			if !yield(ev, nil) {
				stopped = true
				cancel()
				return false
			}
			return true
		}
		var chunks any
		if slices.Contains(modes, runtime.StreamMessages) {
			chunks = emit
		}
		ctx = context.WithValue(ctx, chunkEmitKey{}, chunks)
		_, err := a.invoke(ctx, in, cfg, emit)
		mu.Lock()
		defer mu.Unlock()
		if err != nil && !stopped {
			yield(runtime.Event{}, err)
		}
	}
}

func (a *loopAgent) GetState(ctx context.Context, threadID string) (*runtime.State, error) {
	if a.cfg.Checkpointer == nil {
		return nil, errors.New("agent.GetState: checkpointer is not configured")
	}
	cp, err := a.cfg.Checkpointer.Latest(ctx, threadID)
	if err != nil || cp == nil {
		return nil, err
	}
	return cp.State.Clone(), nil
}
