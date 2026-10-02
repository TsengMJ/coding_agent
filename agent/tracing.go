package agent

import (
	"context"
	"errors"
	"maps"

	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
	"github.com/tsengmingchun/deepagents_golang/tool"
	"github.com/tsengmingchun/deepagents_golang/tracing"
)

// Run input and output keys shared with tracing.Tracer implementations.
const (
	TraceMessagesKey = "messages"
	TraceToolsKey    = "tools"
	TraceMessageKey  = "message"
	TraceOutputKey   = "output"
)

// startAgentRun opens the chain run of one invocation and makes its tracer
// the one nested agents use. It returns a nil Run when tracing is off.
func (a *loopAgent) startAgentRun(ctx context.Context, in Input, cfg runtime.RunConfig) (context.Context, tracing.Run) {
	t := a.cfg.Tracer
	if t == nil {
		t = tracing.FromContext(ctx)
	}
	if t == nil {
		return ctx, nil
	}
	ctx = tracing.WithTracer(ctx, t)
	name := a.cfg.Name
	if name == "" {
		name = "agent"
	}
	md := map[string]any{tracing.AgentTypeKey: "root", tracing.AgentNameKey: name}
	maps.Copy(md, a.cfg.Metadata)
	maps.Copy(md, cfg.Metadata)
	if v, _ := cfg.Configurable[tracing.AgentTypeKey].(string); v != "" {
		md[tracing.AgentTypeKey] = v
	}
	if cfg.ThreadID != "" {
		md[tracing.ThreadIDKey] = cfg.ThreadID
	}
	inputs := map[string]any{TraceMessagesKey: in.Messages}
	if in.Resume != nil {
		inputs["resume"] = in.Resume.Resume
	}
	return t.Start(ctx, tracing.RunInfo{Name: name, Type: tracing.RunChain, Inputs: inputs, Metadata: md, Tags: cfg.Tags})
}

func endAgentRun(run tracing.Run, st *execState, err error) {
	if run == nil {
		return
	}
	out := map[string]any{}
	if st != nil && st.State != nil {
		out[TraceMessagesKey] = st.State.Messages
		if st.State.StructuredResponse != nil {
			out["structured_response"] = st.State.StructuredResponse
		}
	}
	var paused *runtime.InterruptError
	if errors.As(err, &paused) {
		out["interrupted"] = paused.Interrupts
		err = nil
	}
	run.End(out, err)
}

func startModelRun(ctx context.Context, model llm.ChatModel, req llm.ChatRequest) (context.Context, tracing.Run) {
	if tracing.FromContext(ctx) == nil {
		return ctx, nil
	}
	info := llm.ModelForContext(ctx, model).Info()
	md := map[string]any{"ls_provider": info.Provider, "ls_model_name": info.Model}
	return tracing.Start(ctx, tracing.RunInfo{
		Name: modelRunName(info), Type: tracing.RunLLM, Metadata: md,
		Inputs: map[string]any{TraceMessagesKey: req.Messages, TraceToolsKey: req.Tools},
	})
}

func modelRunName(info llm.ModelInfo) string {
	if info.Model == "" {
		return "model"
	}
	return info.Model
}

func endModelRun(run tracing.Run, reply *llm.Message, err error) {
	if run == nil {
		return
	}
	out := map[string]any{}
	if reply != nil {
		out[TraceMessageKey] = *reply
	}
	run.End(out, err)
}

func startToolRun(ctx context.Context, call llm.ToolCall) (context.Context, tracing.Run) {
	if tracing.FromContext(ctx) == nil {
		return ctx, nil
	}
	return tracing.Start(ctx, tracing.RunInfo{
		Name: call.Name, Type: tracing.RunTool, Inputs: call.Args,
		Metadata: map[string]any{"tool_call_id": call.ID},
	})
}

func endToolRun(run tracing.Run, res *tool.Result, err error) {
	if run == nil {
		return
	}
	out := map[string]any{}
	if res != nil {
		out[TraceOutputKey] = resultText(res)
		if res.IsError && err == nil {
			err = errors.New(resultText(res))
		}
	}
	run.End(out, err)
}

// resultText is a tool result's text, including the tool message a Command
// carries (e.g. a subagent's report).
func resultText(res *tool.Result) string {
	if res.Command != nil && res.Command.Update != nil {
		for _, m := range res.Command.Update.Messages {
			if m.Role == llm.RoleTool {
				return m.Text()
			}
		}
	}
	return llm.Message{Content: res.Content}.Text()
}
