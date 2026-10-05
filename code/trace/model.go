package trace

import (
	"context"
	"errors"
	"iter"
	"time"

	"github.com/tsengmingchun/quill/internal/notimpl"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/tool"
)

// Model records every call to the wrapped model made under a ctx carrying
// a recorder, and tags the reply with its trace seq (see SeqOf).
type Model struct {
	llm.ChatModel
}

// WrapModel returns m wrapped in a Model.
func WrapModel(m llm.ChatModel) *Model { return &Model{ChatModel: m} }

func (m *Model) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return m.ChatModel.Generate(ctx, req)
	}
	start := time.Now()
	reply, err := m.ChatModel.Generate(ctx, req)
	seq := m.record(sc, req, reply, start, err)
	if reply != nil && seq > 0 {
		tagged := withSeq(*reply, seq)
		reply = &tagged
	}
	return reply, err
}

func (m *Model) Stream(ctx context.Context, req llm.ChatRequest) iter.Seq2[llm.Chunk, error] {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return m.ChatModel.Stream(ctx, req)
	}
	return func(yield func(llm.Chunk, error) bool) {
		start := time.Now()
		started, done := false, false
		for c, err := range m.ChatModel.Stream(ctx, req) {
			if err != nil {
				// The caller falls back to Generate, which records the call.
				if !started && errors.Is(err, notimpl.ErrNotImplemented) {
					yield(c, err)
					return
				}
				m.record(sc, req, nil, start, err)
				yield(c, err)
				return
			}
			started = true
			if c.Done {
				done = true
				if seq := m.record(sc, req, &c.Delta, start, nil); seq > 0 {
					c.Delta = withSeq(c.Delta, seq)
				}
			}
			if !yield(c, nil) {
				if !done {
					m.record(sc, req, nil, start, context.Canceled)
				}
				return
			}
		}
	}
}

// WebExtract is the namespace part of model calls fetch_url makes to read a
// page.
const WebExtract = "web_extract"

// Labeled records calls to m under the namespace part
// "<label>:<tool call id>", so helper calls a tool makes (e.g. WebExtract)
// show apart from the agent's own. m should already be traced.
func Labeled(m llm.ChatModel, label string) llm.ChatModel {
	return labeled{ChatModel: m, label: label}
}

type labeled struct {
	llm.ChatModel
	label string
}

func (l labeled) scope(ctx context.Context) context.Context {
	return WithNamespace(ctx, l.label+":"+tool.CallIDFrom(ctx))
}

// ForContext keeps a run-selected model selectable through the label.
func (l labeled) ForContext(ctx context.Context) llm.ChatModel {
	return labeled{ChatModel: llm.ModelForContext(ctx, l.ChatModel), label: l.label}
}

func (l labeled) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	return l.ChatModel.Generate(l.scope(ctx), req)
}

func (l labeled) Stream(ctx context.Context, req llm.ChatRequest) iter.Seq2[llm.Chunk, error] {
	return l.ChatModel.Stream(l.scope(ctx), req)
}

// record stores the call and returns its seq, or 0 when recording failed;
// a trace failure never fails the run.
func (m *Model) record(sc scope, req llm.ChatRequest, reply *llm.Message, start time.Time, err error) int {
	info := m.Info()
	model := info.Model
	if info.Provider != "" {
		model = info.Provider + ":" + model
	}
	seq, recErr := sc.rec.RecordLLMCall(LLMCallInput{
		Namespace: sc.ns, Model: model, Request: req, Response: reply,
		Start: start, Duration: time.Since(start), Err: err,
	})
	if recErr != nil {
		sc.rec.warn(recErr)
		return 0
	}
	return seq
}
