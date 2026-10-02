package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

type call struct {
	name, out string
	failed    bool
}

func conversation(calls []call) []llm.Message {
	msgs := []llm.Message{llm.NewUserMessage("fix the bug")}
	for i, c := range calls {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: c.name, Args: map[string]any{"i": i}}}})
		res := llm.NewToolMessage(id, c.name, c.out)
		if c.failed {
			res.Status = llm.ToolStatusError
		}
		msgs = append(msgs, res)
	}
	return msgs
}

func repeat(n int, c call) []call {
	out := make([]call, n)
	for i := range out {
		out[i] = c
	}
	return out
}

// maskedResults lists the indexes (among tool results) that were masked.
func maskedResults(msgs []llm.Message) []int {
	var out []int
	n := 0
	for _, m := range msgs {
		if m.Role != llm.RoleTool {
			continue
		}
		if strings.HasPrefix(m.Text(), "[Old ") {
			out = append(out, n)
		}
		n++
	}
	return out
}

func TestMaskObservations(t *testing.T) {
	big := strings.Repeat("x", maskMinChars)
	read := call{name: "read_file", out: big}
	tests := []struct {
		name  string
		calls []call
		want  []int
	}{
		{"too few results", repeat(maskKeep+maskBatch-1, read), nil},
		{"first batch", repeat(maskKeep+maskBatch, read), seq(0, maskBatch)},
		{"batch boundary holds", repeat(maskKeep+2*maskBatch-1, read), seq(0, maskBatch)},
		{"shell and tests are kept", append([]call{{name: "execute", out: big}, {name: "run_tests", out: big}, {name: "edit_file", out: big}}, repeat(maskKeep+maskBatch-3, read)...), seq(3, maskBatch)},
		{"errors are kept", append([]call{{name: "read_file", out: big, failed: true}}, repeat(maskKeep+maskBatch-1, read)...), seq(1, maskBatch)},
		{"short results are kept", append([]call{{name: "grep", out: "a.go:1: x"}}, repeat(maskKeep+maskBatch-1, read)...), seq(1, maskBatch)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := conversation(tc.calls)
			masked, changed := MaskObservations(msgs)
			if got := maskedResults(masked); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("masked results = %v, want %v", got, tc.want)
			}
			if changed != (len(tc.want) > 0) {
				t.Fatalf("changed = %v", changed)
			}
			if maskedResults(msgs) != nil {
				t.Fatal("input messages were modified")
			}
		})
	}
}

func TestMaskObservationsKeepsToolCallPairs(t *testing.T) {
	msgs := conversation(repeat(maskKeep+maskBatch, call{name: "grep", out: strings.Repeat("y", maskMinChars)}))
	masked, _ := MaskObservations(msgs)
	if len(masked) != len(msgs) {
		t.Fatalf("len = %d, want %d", len(masked), len(msgs))
	}
	for i, m := range masked {
		if m.Role != msgs[i].Role || m.ToolCallID != msgs[i].ToolCallID || len(m.ToolCalls) != len(msgs[i].ToolCalls) {
			t.Fatalf("message %d changed shape: %+v", i, m)
		}
	}
	if got := masked[2].Text(); !strings.Contains(got, "Call grep again") {
		t.Fatalf("placeholder = %q", got)
	}
}

func TestObservationMaskingSendsMaskedRequest(t *testing.T) {
	msgs := conversation(repeat(maskKeep+maskBatch, call{name: "read_file", out: strings.Repeat("z", maskMinChars)}))
	req := &core.ModelRequest{Messages: msgs}
	var sent []llm.Message
	_, err := observationMasking{}.WrapModelCall(context.Background(), req, func(_ context.Context, r *core.ModelRequest) (*core.ModelResponse, error) {
		sent = r.Messages
		return &core.ModelResponse{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(maskedResults(sent)); got != maskBatch {
		t.Fatalf("sent %d masked results, want %d", got, maskBatch)
	}
	if maskedResults(req.Messages) != nil {
		t.Fatal("request messages were modified")
	}
}

func seq(from, to int) []int {
	var out []int
	for i := from; i < to; i++ {
		out = append(out, i)
	}
	return out
}
