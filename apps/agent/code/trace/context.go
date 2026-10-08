package trace

import (
	"context"
	"slices"

	"github.com/tsengmingchun/quill/apps/agent/llm"
)

type scopeKey struct{}

// scope is what the wrappers need to know about the run they serve.
type scope struct {
	rec *Recorder
	ns  []string
	// browser names the browser MCP the run's tools come from.
	browser string
}

// WithRecorder makes model and backend wrappers below ctx record to rec.
func WithRecorder(ctx context.Context, rec *Recorder) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope{rec: rec})
}

// WithNamespace appends part to the namespace of records made below ctx,
// e.g. "task:<tool call id>" for a subagent run.
func WithNamespace(ctx context.Context, part string) context.Context {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return ctx
	}
	sc.ns = append(slices.Clone(sc.ns), part)
	return context.WithValue(ctx, scopeKey{}, sc)
}

// WithBrowserMCP records on every model call below ctx which browser MCP
// ("playwright", "chrome-devtools") the run uses, for A/B comparisons.
func WithBrowserMCP(ctx context.Context, kind string) context.Context {
	sc := scopeFrom(ctx)
	if sc.rec == nil {
		return ctx
	}
	sc.browser = kind
	return context.WithValue(ctx, scopeKey{}, sc)
}

func scopeFrom(ctx context.Context) scope {
	sc, _ := ctx.Value(scopeKey{}).(scope)
	return sc
}

// seqKey is the message metadata key holding the model call's trace seq.
const seqKey = "trace_seq"

// SeqOf returns the trace sequence number of the model call that produced
// msg, including after a JSON round trip.
func SeqOf(msg llm.Message) (int, bool) {
	switch v := msg.Metadata[seqKey].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

func withSeq(msg llm.Message, seq int) llm.Message {
	md := make(map[string]any, len(msg.Metadata)+1)
	for k, v := range msg.Metadata {
		md[k] = v
	}
	md[seqKey] = seq
	msg.Metadata = md
	return msg
}
