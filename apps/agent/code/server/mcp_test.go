package server

import (
	"context"
	"errors"
	"testing"

	"github.com/tsengmingchun/quill/apps/agent/code/storage"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

type fakeMCP struct {
	tools    []tool.Tool
	toolsErr error
	closed   bool
}

func (f *fakeMCP) Tools(context.Context) ([]tool.Tool, error) { return f.tools, f.toolsErr }
func (f *fakeMCP) Close() error                               { f.closed = true; return nil }

func pingTool() tool.Tool {
	return &tool.Func{ToolName: "mcp__fake__ping", ToolSchema: tool.Schema{"type": "object"},
		Fn: func(context.Context, *tool.Runtime, map[string]any) (*tool.Result, error) {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock("pong")}}, nil
		}}
}

func TestServiceRunCanCallMCPTools(t *testing.T) {
	model := llm.NewFakeModel(
		llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "p", Name: "mcp__fake__ping", Args: map[string]any{}}}},
		llm.NewAssistantMessage("done"),
	)
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: model, NoVerifyGate: true, MCP: &fakeMCP{tools: []tool.Tool{pingTool()}}})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	events, err := collect(t, svc, userReq("t1", t.TempDir(), "ping"))
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for _, m := range messages(events) {
		if m.ToolCallID == "p" {
			out = m.Text()
		}
	}
	if out != "pong" {
		t.Fatalf("mcp tool output = %q", out)
	}
}

func TestServiceCloseClosesMCP(t *testing.T) {
	m := &fakeMCP{}
	svc, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: llm.NewFakeModel(), MCP: m})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	if !m.closed {
		t.Fatal("MCP session not closed")
	}
}

func TestServiceNewFailsWhenMCPToolsFail(t *testing.T) {
	_, err := New(context.Background(), Config{Storage: storage.NewMemory(), Model: llm.NewFakeModel(), MCP: &fakeMCP{toolsErr: errors.New("boom")}})
	if err == nil {
		t.Fatal("New succeeded")
	}
}
