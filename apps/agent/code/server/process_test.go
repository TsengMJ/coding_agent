package server

import (
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/process"
	"github.com/tsengmingchun/quill/apps/agent/llm"
)

func TestBackgroundProcessesOutliveTheRunAndStopWithTheService(t *testing.T) {
	svc, _ := newService(t,
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "p", Name: process.StartName, Args: map[string]any{"command": "sleep 30", "wait_seconds": 0}}}},
		llm.NewAssistantMessage("started"),
	)
	if _, err := collect(t, svc, RunRequest{ThreadID: "t1", Cwd: t.TempDir(), Interactive: true, AutoApprove: true, Messages: []llm.Message{llm.NewUserMessage("start it")}}); err != nil {
		t.Fatal(err)
	}
	proc, ok := svc.processes.Get("t1", "p1")
	if !ok {
		t.Fatal("the run did not start a process for its thread")
	}
	if running, _ := proc.Status(); !running {
		t.Fatal("process ended with the run")
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if running, _ := proc.Status(); running {
		t.Fatal("process outlived the service")
	}
}
