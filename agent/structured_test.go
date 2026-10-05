package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tsengmingchun/quill/internal/notimpl"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

var weatherSchema = tool.Schema{
	"title": "Weather",
	"type":  "object",
	"properties": map[string]any{
		"city":    map[string]any{"type": "string"},
		"celsius": map[string]any{"type": "integer"},
		"sky":     map[string]any{"type": "string", "enum": []any{"sunny", "cloudy"}},
	},
	"required":             []any{"city", "celsius"},
	"additionalProperties": false,
}

func structuredCall(id string, args map[string]any) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: id, Name: "Weather", Args: args}}}
}

func runStructured(t *testing.T, f ResponseFormat, replies ...llm.Message) (*runtime.State, *llm.FakeModel) {
	t.Helper()
	model := llm.NewFakeModel(replies...)
	a, err := CreateAgent(Config{Model: model, ResponseFormat: f})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Invoke(context.Background(), Input{Messages: []llm.Message{llm.NewUserMessage("weather?")}}, runtime.RunConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return st, model
}

func TestToolStrategySetsStructuredResponseAndEnds(t *testing.T) {
	args := map[string]any{"city": "Taipei", "celsius": 30, "sky": "sunny"}
	st, model := runStructured(t, ToolStrategy{Schema: weatherSchema}, structuredCall("s1", args))
	if !reflect.DeepEqual(st.StructuredResponse, args) {
		t.Fatalf("structured response = %#v", st.StructuredResponse)
	}
	if n := len(model.Requests()); n != 1 {
		t.Fatalf("model calls = %d, want the run to end after the structured response", n)
	}
	var offered bool
	for _, s := range model.Requests()[0].Tools {
		offered = offered || s.Name == "Weather"
	}
	if !offered {
		t.Fatal("the structured output tool was not offered")
	}
	if last := st.Messages[len(st.Messages)-1]; last.Role != llm.RoleTool || !strings.Contains(last.Text(), "Returning structured response") {
		t.Fatalf("last message = %+v", last)
	}
}

func TestToolStrategyRetriesInvalidArguments(t *testing.T) {
	good := map[string]any{"city": "Taipei", "celsius": 30}
	st, model := runStructured(t, AutoStrategy{Schema: weatherSchema},
		structuredCall("s1", map[string]any{"city": "Taipei", "celsius": 30.5, "sky": "rain", "wind": 3}),
		structuredCall("s2", good),
	)
	if !reflect.DeepEqual(st.StructuredResponse, good) || len(model.Requests()) != 2 {
		t.Fatalf("structured response = %#v after %d calls", st.StructuredResponse, len(model.Requests()))
	}
	errMsg := st.Messages[2]
	for _, want := range []string{"$.celsius must be integer", "$.sky must be one of", "$.wind is not allowed"} {
		if errMsg.Status != llm.ToolStatusError || !strings.Contains(errMsg.Text(), want) {
			t.Fatalf("error message lacks %q: %+v", want, errMsg)
		}
	}
}

func TestToolStrategyRejectsSeveralCallsInOneTurn(t *testing.T) {
	good := map[string]any{"city": "A", "celsius": 1}
	twice := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
		{ID: "a", Name: "Weather", Args: good}, {ID: "b", Name: "Weather", Args: good},
	}}
	st, _ := runStructured(t, ToolStrategy{Schema: weatherSchema}, twice, structuredCall("c", good))
	if st.Messages[2].Status != llm.ToolStatusError || !strings.Contains(st.Messages[2].Text(), "exactly once") {
		t.Fatalf("message = %+v", st.Messages[2])
	}
	if !reflect.DeepEqual(st.StructuredResponse, good) {
		t.Fatalf("structured response = %#v", st.StructuredResponse)
	}
}

func TestToolStrategyWithoutTitleUsesDefaultName(t *testing.T) {
	st, model := runStructured(t, ToolStrategy{Schema: tool.Schema{"type": "object"}},
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "s", Name: DefaultStructuredToolName, Args: map[string]any{"x": "y"}}}})
	if st.StructuredResponse == nil || model.Requests()[0].Tools[0].Name != DefaultStructuredToolName {
		t.Fatalf("structured response = %#v", st.StructuredResponse)
	}
}

func TestProviderStrategyIsNotImplemented(t *testing.T) {
	_, err := CreateAgent(Config{Model: llm.NewFakeModel(), ResponseFormat: ProviderStrategy{Schema: weatherSchema}})
	if !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("err = %v", err)
	}
}
