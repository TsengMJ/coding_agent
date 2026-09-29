package llm

import (
	"encoding/json"
	"maps"
	"time"
)

// Metadata keys the agent loop sets on assistant messages (one model call)
// and tool messages (one tool call). Values are Unix milliseconds and
// milliseconds so they survive a JSON round trip.
const (
	MetaStartedAtMS = "started_at_ms"
	MetaDurationMS  = "duration_ms"
)

// Timing is when a model or tool call started and how long it took.
type Timing struct {
	Start    time.Time
	Duration time.Duration
}

// End is when the call finished.
func (t Timing) End() time.Time { return t.Start.Add(t.Duration) }

// WithTiming returns msg with t recorded in a copy of its Metadata.
func WithTiming(msg Message, t Timing) Message {
	meta := maps.Clone(msg.Metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[MetaStartedAtMS] = t.Start.UnixMilli()
	meta[MetaDurationMS] = t.Duration.Milliseconds()
	msg.Metadata = meta
	return msg
}

// TimingOf reads the timing recorded by WithTiming, including after the
// metadata went through JSON (numbers become float64 or json.Number).
func TimingOf(msg Message) (Timing, bool) {
	start, ok1 := metaInt(msg.Metadata[MetaStartedAtMS])
	dur, ok2 := metaInt(msg.Metadata[MetaDurationMS])
	if !ok1 || !ok2 {
		return Timing{}, false
	}
	return Timing{Start: time.UnixMilli(start), Duration: time.Duration(dur) * time.Millisecond}, true
}

func metaInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
