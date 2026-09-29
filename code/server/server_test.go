package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/internal/notimpl"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func init() {
	err := llm.RegisterProvider("server-test", func(string, map[string]any) (llm.ChatModel, error) {
		return llm.NewFakeModel(llm.NewAssistantMessage("pong")), nil
	})
	if err != nil {
		panic(err)
	}
}

func TestInProcessRunYieldsFinalState(t *testing.T) {
	srv, err := New(context.Background(), Config{ModelSpec: "server-test:m"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	var events []runtime.Event
	for ev, err := range srv.Run(context.Background(), RunRequest{Input: agent.Input{Messages: []llm.Message{llm.NewUserMessage("ping")}}}) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	if len(events) != 1 || events[0].Mode != runtime.StreamValues {
		t.Fatalf("unexpected events: %+v", events)
	}
	state := events[0].Data.(*runtime.State)
	if got := state.Messages[len(state.Messages)-1].Text(); got != "pong" {
		t.Fatalf("final text = %q", got)
	}
}

func TestResolveModelSpecPrecedence(t *testing.T) {
	t.Setenv(ModelEnvVar, "env:model")
	if got := resolveModelSpec(Config{ModelSpec: "flag:model"}); got != "flag:model" {
		t.Errorf("flag should win, got %q", got)
	}
	if got := resolveModelSpec(Config{}); got != "env:model" {
		t.Errorf("env should win over default, got %q", got)
	}
	t.Setenv(ModelEnvVar, "")
	if got := resolveModelSpec(Config{}); got != DefaultModelSpec {
		t.Errorf("want default, got %q", got)
	}
}

func TestNewRejectsRemoteAddr(t *testing.T) {
	if _, err := New(context.Background(), Config{Addr: ":8123"}); !errors.Is(err, notimpl.ErrNotImplemented) {
		t.Fatalf("want ErrNotImplemented, got %v", err)
	}
}
