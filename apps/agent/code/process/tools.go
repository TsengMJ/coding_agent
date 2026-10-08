package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

// Tool names.
const (
	StartName = "start_process"
	ReadName  = "read_process_output"
	StopName  = "stop_process"
)

const (
	defaultStartWait = 5
	maxWait          = 60
)

// Tools returns start_process, read_process_output and stop_process,
// running commands through p and keeping processes per thread in m.
func Tools(m *Manager, p Preparer) []tool.Tool {
	return []tool.Tool{
		&tool.Func{
			ToolName: StartName,
			ToolDescription: "Start a long-running command in the background, such as a dev server (`npm run dev`), and return its first output. " +
				"Unlike execute, the process keeps running after the call. Use read_process_output to see later output and stop_process when done. " +
				"It runs in the project root with the same sandbox as execute.",
			ToolSchema: tool.Schema{"type": "object", "required": []any{"command"}, "properties": map[string]any{
				"command":      map[string]any{"type": "string", "description": "Shell command to run."},
				"wait_seconds": map[string]any{"type": "integer", "description": fmt.Sprintf("Seconds to wait for startup output before returning (default %d, max %d); returns early if the process exits.", defaultStartWait, maxWait)},
			}},
			Fn: func(_ context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
				command, _ := args["command"].(string)
				if strings.TrimSpace(command) == "" {
					return failed("command must be a non-empty string"), nil
				}
				proc, err := m.Start(thread(rt), p, command)
				if err != nil {
					return failed(fmt.Sprintf("cannot start the process: %v", err)), nil
				}
				proc.Wait(waitFor(args, defaultStartWait))
				return report(proc, fmt.Sprintf("Started process %s: %s", proc.ID, command)), nil
			},
		},
		&tool.Func{
			ToolName:        ReadName,
			ToolDescription: "Return the output a background process printed since the last read, and whether it is still running.",
			ToolSchema: tool.Schema{"type": "object", "required": []any{"id"}, "properties": map[string]any{
				"id":           map[string]any{"type": "string", "description": "Process id from start_process, e.g. p1."},
				"wait_seconds": map[string]any{"type": "integer", "description": fmt.Sprintf("Seconds to wait first, to collect more output (default 0, max %d); returns early if the process exits.", maxWait)},
			}},
			ToolEffect: func(map[string]any) tool.Effect { return tool.Effect{Kind: tool.EffectRead} },
			Fn: func(_ context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
				id, _ := args["id"].(string)
				proc, ok := m.Get(thread(rt), id)
				if !ok {
					return failed(fmt.Sprintf("no process %s in this thread", id)), nil
				}
				proc.Wait(waitFor(args, 0))
				return report(proc, fmt.Sprintf("Process %s: %s", proc.ID, proc.Command)), nil
			},
		},
		&tool.Func{
			ToolName:        StopName,
			ToolDescription: "Stop a background process started with start_process, together with everything it started.",
			ToolSchema: tool.Schema{"type": "object", "required": []any{"id"}, "properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "Process id from start_process, e.g. p1."},
			}},
			Fn: func(_ context.Context, rt *tool.Runtime, args map[string]any) (*tool.Result, error) {
				id, _ := args["id"].(string)
				if err := m.Stop(thread(rt), id); err != nil {
					return failed(err.Error()), nil
				}
				proc, _ := m.Get(thread(rt), id)
				out, omitted := proc.Read()
				return text(fmt.Sprintf("Process %s stopped.%s", id, outputSection(out, omitted))), nil
			},
		},
	}
}

func thread(rt *tool.Runtime) string {
	if rt == nil || rt.Config.ThreadID == "" {
		return "default"
	}
	return rt.Config.ThreadID
}

func waitFor(args map[string]any, def int) time.Duration {
	secs := def
	switch v := args["wait_seconds"].(type) {
	case float64:
		secs = int(v)
	case int:
		secs = v
	}
	return time.Duration(min(max(secs, 0), maxWait)) * time.Second
}

func report(proc *Proc, header string) *tool.Result {
	status := "Status: running"
	if running, code := proc.Status(); !running {
		status = fmt.Sprintf("Status: exited with code %d", code)
	}
	out, omitted := proc.Read()
	return text(header + "\n" + status + outputSection(out, omitted))
}

func outputSection(out string, omitted int64) string {
	if out == "" {
		return "\n(no new output)"
	}
	if omitted > 0 {
		return fmt.Sprintf("\nOutput (%d earlier bytes dropped):\n%s", omitted, out)
	}
	return "\nOutput:\n" + out
}

func text(s string) *tool.Result {
	return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(s)}}
}

func failed(s string) *tool.Result {
	return &tool.Result{IsError: true, Content: []llm.ContentBlock{llm.TextBlock(s)}}
}
