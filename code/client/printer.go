package client

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/tsengmingchun/quill/code/server"
	"github.com/tsengmingchun/quill/code/sessions"
	"github.com/tsengmingchun/quill/llm"
)

// toolCallArgKeys lists, in priority order, the argument shown for a tool call.
var toolCallArgKeys = []string{"file_path", "command", "path", "pattern", "library_name", "query", "url", "task_id"}

// printer renders run events as plain text: assistant text on out, tool
// activity on errw.
type printer struct {
	out, errw io.Writer
	// noStream ignores deltas and prints each assistant message whole.
	noStream bool
	// streaming is true while deltas of an assistant message are being printed.
	streaming bool
}

func (p *printer) handle(ev server.Event) {
	if len(ev.Namespace) > 0 {
		// Plain text output shows only the main agent; a subagent's work
		// surfaces as its task tool result.
		return
	}
	switch ev.Kind {
	case server.EventDelta:
		if p.noStream {
			return
		}
		fmt.Fprint(p.out, ev.Text)
		p.streaming = true
	case server.EventMessage:
		p.message(*ev.Message)
	}
}

func (p *printer) message(m llm.Message) {
	switch m.Role {
	case llm.RoleAssistant:
		if p.streaming {
			fmt.Fprintln(p.out)
			p.streaming = false
		} else if text := strings.TrimSpace(m.Text()); text != "" {
			fmt.Fprintln(p.out, text)
		}
		for _, call := range m.ToolCalls {
			fmt.Fprintln(p.errw, ToolCallLine(call))
		}
	case llm.RoleTool:
		if m.Status == llm.ToolStatusError {
			fmt.Fprintf(p.errw, "[tool error] %s: %s\n", m.Name, firstLine(strings.TrimSpace(m.Text())))
		}
	}
}

// ToolCallLine renders "[tool] name arg" for a tool call.
func ToolCallLine(call llm.ToolCall) string {
	line := "[tool] " + call.Name
	if arg := ToolCallArg(call); arg != "" {
		line += " " + arg
	}
	return line
}

// ToolCallArg returns the most informative argument of a tool call, first line only.
func ToolCallArg(call llm.ToolCall) string {
	if call.Name == "query_docs" {
		id, _ := call.Args["library_id"].(string)
		q, _ := call.Args["query"].(string)
		return firstLine(id + " · " + q)
	}
	for _, key := range toolCallArgKeys {
		if v, ok := call.Args[key].(string); ok && v != "" {
			return firstLine(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

// WriteJSON writes the one-line machine-readable envelope used by `--json`
// commands (Python `output.write_json`).
func WriteJSON(w io.Writer, command string, data any) error {
	envelope := struct {
		SchemaVersion int    `json:"schema_version"`
		Command       string `json:"command"`
		Data          any    `json:"data"`
	}{1, command, data}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(envelope)
}

// PrintThreads writes a thread table; showCwd adds the directory column.
func PrintThreads(w io.Writer, list []sessions.Info, showCwd bool) {
	if len(list) == 0 {
		fmt.Fprintln(w, "no threads")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "THREAD\tUPDATED\tMSGS\tTITLE"
	if showCwd {
		header += "\tCWD"
	}
	fmt.Fprintln(tw, header)
	for _, t := range list {
		row := fmt.Sprintf("%s\t%s\t%d\t%s", t.ThreadID, t.UpdatedAt.Local().Format(time.DateTime), t.MessageCount, t.Title)
		if showCwd {
			row += "\t" + t.Cwd
		}
		fmt.Fprintln(tw, row)
	}
	_ = tw.Flush()
}
