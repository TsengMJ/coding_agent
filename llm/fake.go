package llm

import (
	"context"
	"errors"
	"iter"
	"sync"
)

// ErrScriptExhausted is returned by FakeModel once every scripted reply has
// been consumed.
var ErrScriptExhausted = errors.New("fake model: script exhausted")

// FakeModel replays scripted assistant messages in order and records every
// request it receives. It is intended for tests.
type FakeModel struct {
	mu       sync.Mutex
	script   []Message
	next     int
	requests []ChatRequest
}

// NewFakeModel returns a FakeModel that replies with replies in order.
func NewFakeModel(replies ...Message) *FakeModel {
	return &FakeModel{script: replies}
}

// Info implements ChatModel.
func (f *FakeModel) Info() ModelInfo { return ModelInfo{Provider: "fake", Model: "fake"} }

// Profile implements ChatModel.
func (f *FakeModel) Profile() ModelProfile { return ModelProfile{} }

// Generate implements ChatModel.
func (f *FakeModel) Generate(ctx context.Context, req ChatRequest) (*Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.next >= len(f.script) {
		return nil, ErrScriptExhausted
	}
	reply := f.script[f.next]
	f.next++
	if reply.Role == "" {
		reply.Role = RoleAssistant
	}
	return &reply, nil
}

// Stream implements ChatModel. It replays the next reply's text word by word,
// then yields the whole reply as the Done chunk.
func (f *FakeModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		reply, err := f.Generate(ctx, req)
		if err != nil {
			yield(Chunk{}, err)
			return
		}
		for _, piece := range splitWords(reply.Text()) {
			if !yield(Chunk{Delta: NewAssistantMessage(piece)}, nil) {
				return
			}
		}
		yield(Chunk{Delta: *reply, Done: true}, nil)
	}
}

// splitWords splits s after each run of spaces, so the pieces concatenate to s.
func splitWords(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' && (i+1 == len(s) || s[i+1] != ' ') {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Requests returns a copy of the requests received so far.
func (f *FakeModel) Requests() []ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ChatRequest(nil), f.requests...)
}
