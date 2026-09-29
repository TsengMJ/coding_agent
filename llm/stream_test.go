package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeOpenAIStream serves an SSE response built from data payloads.
func fakeOpenAIStream(t *testing.T, payloads []string, captured *map[string]any) *OpenAIModel {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, captured)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, p := range payloads {
			_, _ = io.WriteString(w, "data: "+p+"\n\n")
			w.(http.Flusher).Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIModel(ProviderConfig{Model: "gpt-test", APIKey: "sk-test", BaseURL: srv.URL}, false)
}

func collectStream(t *testing.T, m ChatModel) ([]string, *Message) {
	t.Helper()
	var deltas []string
	var final *Message
	for c, err := range m.Stream(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}) {
		if err != nil {
			t.Fatal(err)
		}
		if c.Done {
			final = &c.Delta
			continue
		}
		deltas = append(deltas, c.Delta.Text())
	}
	if final == nil {
		t.Fatal("no Done chunk")
	}
	return deltas, final
}

func TestOpenAIStreamYieldsTextDeltas(t *testing.T) {
	var body map[string]any
	m := fakeOpenAIStream(t, []string{
		`{"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
	}, &body)
	deltas, final := collectStream(t, m)
	if strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("deltas = %q", deltas)
	}
	if final.Text() != "Hello" || final.Role != RoleAssistant {
		t.Fatalf("final = %+v", final)
	}
	if body["stream"] != true {
		t.Fatalf("request did not ask for a stream: %v", body["stream"])
	}
}

func TestOpenAIStreamAssemblesToolCallsByIndex(t *testing.T) {
	m := fakeOpenAIStream(t, []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"write_file","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"{\"file_path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":"function","function":{"arguments":"\"/a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","type":"function","function":{"name":"execute","arguments":"{\"command\":\"ls\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}, nil)
	deltas, final := collectStream(t, m)
	if len(deltas) != 0 {
		t.Fatalf("tool-call chunks must not produce text deltas: %q", deltas)
	}
	if len(final.ToolCalls) != 2 {
		t.Fatalf("tool calls = %+v", final.ToolCalls)
	}
	first, second := final.ToolCalls[0], final.ToolCalls[1]
	if first.ID != "c1" || first.Name != "write_file" || first.Args["file_path"] != "/a.txt" {
		t.Fatalf("first call = %+v", first)
	}
	if second.ID != "c2" || second.Name != "execute" || second.Args["command"] != "ls" {
		t.Fatalf("second call = %+v", second)
	}
}

func TestOpenAIStreamStopsWhenConsumerBreaks(t *testing.T) {
	m := fakeOpenAIStream(t, []string{
		`{"choices":[{"delta":{"content":"a"}}]}`,
		`{"choices":[{"delta":{"content":"b"}}]}`,
	}, nil)
	for range m.Stream(context.Background(), ChatRequest{Messages: []Message{NewUserMessage("hi")}}) {
		break
	}
}

func TestFakeModelStreamPiecesConcatenateToReply(t *testing.T) {
	deltas, final := collectStream(t, NewFakeModel(NewAssistantMessage("one two  three")))
	if strings.Join(deltas, "") != "one two  three" || len(deltas) != 3 {
		t.Fatalf("deltas = %q", deltas)
	}
	if final.Text() != "one two  three" {
		t.Fatalf("final = %q", final.Text())
	}
}
