package server

import (
	"context"
	"fmt"
	"iter"
	"maps"

	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
)

// ModelNameKey is the reply Metadata key naming the "provider:model" spec
// that produced an assistant message.
const ModelNameKey = subagents.ModelNameKey

// runModel is the model chosen for one run.
type runModel struct {
	spec  string
	model llm.ChatModel
}

type runModelKey struct{}

func withRunModel(ctx context.Context, m runModel) context.Context {
	return context.WithValue(ctx, runModelKey{}, m)
}

// switchModel lets cached agents run each call on the model chosen for the
// current run (withRunModel), falling back to def, so switching models does
// not rebuild agents. Subagents share the run's ctx and switch with it.
// Info and Profile describe def.
type switchModel struct {
	def runModel
}

func (m *switchModel) pick(ctx context.Context) runModel {
	if rm, ok := ctx.Value(runModelKey{}).(runModel); ok && rm.model != nil {
		return rm
	}
	return m.def
}

func (m *switchModel) Info() llm.ModelInfo       { return m.def.model.Info() }
func (m *switchModel) Profile() llm.ModelProfile { return m.def.model.Profile() }

func (m *switchModel) ForContext(ctx context.Context) llm.ChatModel { return m.pick(ctx).model }

func (m *switchModel) Generate(ctx context.Context, req llm.ChatRequest) (*llm.Message, error) {
	rm := m.pick(ctx)
	reply, err := rm.model.Generate(ctx, req)
	if err != nil || reply == nil {
		return reply, err
	}
	tagged := tagModel(*reply, rm.spec)
	return &tagged, nil
}

func (m *switchModel) Stream(ctx context.Context, req llm.ChatRequest) iter.Seq2[llm.Chunk, error] {
	rm := m.pick(ctx)
	return func(yield func(llm.Chunk, error) bool) {
		for c, err := range rm.model.Stream(ctx, req) {
			if err == nil && c.Done {
				c.Delta = tagModel(c.Delta, rm.spec)
			}
			if !yield(c, err) {
				return
			}
		}
	}
}

func tagModel(msg llm.Message, spec string) llm.Message {
	msg.Metadata = maps.Clone(msg.Metadata)
	if msg.Metadata == nil {
		msg.Metadata = map[string]any{}
	}
	msg.Metadata[ModelNameKey] = spec
	return msg
}

func specOf(m llm.ChatModel) string {
	info := m.Info()
	if info.Provider == "" {
		return info.Model
	}
	return info.Provider + ":" + info.Model
}

// modelFor returns the traced model for spec, building it once.
func (s *Service) modelFor(spec string) (runModel, error) {
	if spec == "" || spec == s.defaultModel.spec {
		return s.defaultModel, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.models[spec]; ok {
		return runModel{spec: spec, model: m}, nil
	}
	m, err := s.newModel(spec)
	if err != nil {
		return runModel{}, fmt.Errorf("%w: model %q: %v", ErrBadRequest, spec, err)
	}
	m = trace.WrapModel(m)
	s.models[spec] = m
	return runModel{spec: spec, model: m}, nil
}
