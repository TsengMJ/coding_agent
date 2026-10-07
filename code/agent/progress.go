package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

// progressMarker starts every note the monitor adds to a tool result.
const progressMarker = "[progress]"

// volatile matches the parts of tool output that change between identical
// runs: numbers (durations, counts, line offsets, ports) and hex ids.
var volatile = regexp.MustCompile(`0x[0-9a-fA-F]+|[0-9a-f]{12,}|\d+(?:\.\d+)?`)

// ProgressMonitor stops the agent from repeating a call that keeps failing
// the same way. Everything is derived from the conversation, so it survives
// a checkpoint restore and replays offline on recorded traces.
type ProgressMonitor struct{}

func (ProgressMonitor) Name() string { return "ProgressMonitorMiddleware" }

func (ProgressMonitor) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	var history []llm.Message
	if req.State != nil {
		history = req.State.Messages
	}
	if msg := RepeatRefusal(history, req.ToolCall); msg != "" {
		return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(msg)}, IsError: true}, nil
	}
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	status := llm.ToolStatusSuccess
	if res.IsError {
		status = llm.ToolStatusError
	}
	if note := ProgressNote(history, req.ToolCall, resultText(res), status); note != "" {
		res.Content = append(slices.Clone(res.Content), llm.TextBlock("\n\n"+note))
	}
	return res, nil
}

// RepeatRefusal returns the error to send instead of running call when the
// same call already failed twice in a row the same way since anything
// changed, or "" to run it.
func RepeatRefusal(history []llm.Message, call llm.ToolCall) string {
	fails := repeatFailures(history, call)
	if len(fails) < 2 || fails[len(fails)-1] != fails[len(fails)-2] {
		return ""
	}
	return fmt.Sprintf("%s Not run: this exact %s call failed twice with the same result and nothing changed since. "+
		"Running it again will fail the same way. Read the error, change the arguments or the code, or try another approach.",
		progressMarker, call.Name)
}

// ProgressNote returns a note to append to a tool result when it repeats
// the failure of the previous identical call, or "".
func ProgressNote(history []llm.Message, call llm.ToolCall, text string, status llm.ToolStatus) string {
	fp, failed := failureFingerprint(call, text, status)
	if !failed {
		return ""
	}
	if fails := repeatFailures(history, call); len(fails) == 0 || fails[len(fails)-1] != fp {
		return ""
	}
	return progressMarker + " This is the same failure as your previous identical call. " +
		"Change something before running it again; an unchanged repeat will be refused."
}

// repeatFailures lists the failure fingerprints of earlier calls identical
// to call since the last change: a user turn, a successful edit or a
// successful shell command. An identical call that succeeded clears it.
func repeatFailures(history []llm.Message, call llm.ToolCall) []string {
	key := callKey(call)
	var fails []string
	calls := map[string]llm.ToolCall{}
	for _, m := range history {
		if rt.IsUserTurn(m) {
			fails = nil
		}
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		if m.Role != llm.RoleTool {
			continue
		}
		c, ok := calls[m.ToolCallID]
		if !ok {
			continue
		}
		text := m.Text()
		fp, failed := failureFingerprint(c, text, m.Status)
		switch {
		case callKey(c) == key && failed:
			fails = append(fails, fp)
		case callKey(c) == key || !failed && changesWorkspace(c):
			fails = nil
		}
	}
	return fails
}

// failureFingerprint identifies how a call failed, ignoring numbers that
// vary between identical runs. failed is false for a successful result.
func failureFingerprint(call llm.ToolCall, text string, status llm.ToolStatus) (fp string, failed bool) {
	if s, ok := ClassifyFailure(call, text); ok {
		if s.Category == FailurePassed {
			return "", false
		}
		return s.Key(), true
	}
	if status == llm.ToolStatusError || strings.HasPrefix(text, "Error") {
		return normalizeOutput(text), true
	}
	if call.Name == executeToolName {
		if m := execExit.FindStringSubmatch(text); m != nil && m[1] != "0" {
			return normalizeOutput(text), true
		}
	}
	return "", false
}

func normalizeOutput(text string) string {
	text = strings.TrimSpace(stripNotes(text))
	return volatile.ReplaceAllString(text, "#")
}

// changesWorkspace reports whether a successful call may have changed what
// a repeated call sees: edits and shell commands.
func changesWorkspace(c llm.ToolCall) bool {
	return changesFiles(c.Name) || c.Name == executeToolName
}

func callKey(c llm.ToolCall) string {
	raw, _ := json.Marshal(c.Args)
	return c.Name + "\x00" + string(raw)
}
