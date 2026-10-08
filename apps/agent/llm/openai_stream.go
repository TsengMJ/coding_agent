package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Stream implements ChatModel. Text deltas are yielded as they arrive; the
// final chunk (Done) carries the complete message.
func (m *OpenAIModel) Stream(ctx context.Context, req ChatRequest) iter.Seq2[Chunk, error] {
	return func(yield func(Chunk, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		texts := make(chan string, 64)
		type result struct {
			msg *Message
			err error
		}
		done := make(chan result, 1)
		sink := &streamSink{onText: func(s string) {
			select {
			case texts <- s:
			case <-ctx.Done():
			}
		}}
		go func() {
			msg, err := m.generate(withStreamSink(ctx, sink), req, sink)
			close(texts)
			done <- result{msg, err}
		}()
		for s := range texts {
			if !yield(Chunk{Delta: NewAssistantMessage(s)}, nil) {
				cancel()
				for range texts {
				}
				<-done
				return
			}
		}
		r := <-done
		if r.err != nil {
			yield(Chunk{}, r.err)
			return
		}
		yield(Chunk{Delta: *r.msg, Done: true}, nil)
	}
}

type streamSinkKey struct{}

func withStreamSink(ctx context.Context, s *streamSink) context.Context {
	return context.WithValue(ctx, streamSinkKey{}, s)
}

func streamSinkFrom(ctx context.Context) *streamSink {
	s, _ := ctx.Value(streamSinkKey{}).(*streamSink)
	return s
}

// streamSink receives what sseTee parses out of a Chat Completions stream.
// Tool calls are assembled by index here because langchaingo appends a new
// call whenever a delta repeats "type", which some providers do.
type streamSink struct {
	onText func(string)
	// started is set once text or a tool call delta has streamed.
	started atomic.Bool

	mu    sync.Mutex
	calls map[int]*partialCall
}

type partialCall struct {
	id, name string
	args     strings.Builder
}

func (s *streamSink) addToolDelta(index int, id, name, args string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.calls == nil {
		s.calls = map[int]*partialCall{}
	}
	c := s.calls[index]
	if c == nil {
		c = &partialCall{}
		s.calls[index] = c
	}
	if id != "" {
		c.id = id
	}
	if name != "" {
		c.name = name
	}
	c.args.WriteString(args)
}

// toolCalls returns the assembled calls in index order, or nil if none streamed.
func (s *streamSink) toolCalls() []ToolCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := make([]int, 0, len(s.calls))
	for i := range s.calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []ToolCall
	for _, i := range idx {
		c := s.calls[i]
		args := map[string]any{}
		if raw := strings.TrimSpace(c.args.String()); raw != "" {
			_ = json.Unmarshal([]byte(raw), &args)
		}
		out = append(out, ToolCall{ID: c.id, Name: c.name, Args: args})
	}
	return out
}

func isEventStream(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
}

// sseTee passes a Chat Completions SSE body through unchanged while feeding
// each complete "data:" line to a streamSink.
type sseTee struct {
	r    io.ReadCloser
	sink *streamSink
	buf  []byte
}

func (t *sseTee) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if n > 0 {
		t.buf = append(t.buf, p[:n]...)
		for {
			i := bytes.IndexByte(t.buf, '\n')
			if i < 0 {
				break
			}
			t.line(bytes.TrimSpace(t.buf[:i]))
			t.buf = t.buf[i+1:]
		}
	}
	return n, err
}

func (t *sseTee) Close() error { return t.r.Close() }

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

func (t *sseTee) line(line []byte) {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok {
		return
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "[DONE]" {
		return
	}
	var c sseChunk
	if json.Unmarshal(data, &c) != nil || len(c.Choices) == 0 {
		return
	}
	d := c.Choices[0].Delta
	if d.Content != "" || len(d.ToolCalls) > 0 {
		t.sink.started.Store(true)
	}
	if d.Content != "" && t.sink.onText != nil {
		t.sink.onText(d.Content)
	}
	for i, tc := range d.ToolCalls {
		index := i
		if tc.Index != nil {
			index = *tc.Index
		}
		t.sink.addToolDelta(index, tc.ID, tc.Function.Name, tc.Function.Arguments)
	}
}
