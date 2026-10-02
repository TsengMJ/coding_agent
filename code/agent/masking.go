package agent

import (
	"context"
	"fmt"

	core "github.com/tsengmingchun/deepagents_golang/agent"
	"github.com/tsengmingchun/deepagents_golang/code/codetools"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/filesystem"
)

const (
	// maskKeep is how many of the latest tool results are always sent.
	maskKeep = 10
	// maskBatch is how many results become maskable at once. Masking
	// rewrites old messages, which ends the provider's cached prefix there;
	// masking in groups keeps the prefix stable between groups.
	maskBatch = 20
	// maskMinChars leaves short results alone; a placeholder would save
	// little.
	maskMinChars = 600
)

// maskableTools are read-only tools whose output can be produced again by
// calling them with the same arguments, which the assistant message keeps.
var maskableTools = map[string]bool{
	string(filesystem.ToolReadFile): true,
	string(filesystem.ToolGrep):     true,
	string(filesystem.ToolGlob):     true,
	string(filesystem.ToolLs):       true,
	codetools.GitDiffName:           true,
	codetools.GitStatusName:         true,
}

// MaskObservations replaces the output of old read-only tool calls with a
// placeholder. The latest maskKeep results, errors, and output of tools
// that cannot simply be rerun (shell, tests, edits) are kept, as are all
// user and assistant messages, so every tool call still has its result.
// msgs is not modified; changed reports whether anything was masked.
func MaskObservations(msgs []llm.Message) (masked []llm.Message, changed bool) {
	var results []int
	for i, m := range msgs {
		if m.Role == llm.RoleTool {
			results = append(results, i)
		}
	}
	old := (len(results) - maskKeep) / maskBatch * maskBatch
	if old <= 0 {
		return msgs, false
	}
	names := map[string]string{}
	for _, m := range msgs[:results[old-1]] {
		for _, c := range m.ToolCalls {
			names[c.ID] = c.Name
		}
	}
	var out []llm.Message
	for _, i := range results[:old] {
		m := msgs[i]
		name := names[m.ToolCallID]
		size := len(m.Text())
		if !maskableTools[name] || m.Status == llm.ToolStatusError || size < maskMinChars {
			continue
		}
		if out == nil {
			out = append([]llm.Message(nil), msgs...)
		}
		m.Content = []llm.ContentBlock{llm.TextBlock(fmt.Sprintf(
			"[Old %s output (%d characters) removed to save context. Call %s again with the same arguments if you need it.]", name, size, name))}
		out[i] = m
	}
	if out == nil {
		return msgs, false
	}
	return out, true
}

// observationMasking sends masked tool outputs to the model; the stored
// conversation keeps the full outputs.
type observationMasking struct{}

func (observationMasking) Name() string { return "ObservationMaskingMiddleware" }

func (observationMasking) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	masked, changed := MaskObservations(req.Messages)
	if !changed {
		return next(ctx, req)
	}
	updated := *req
	updated.Messages = masked
	return next(ctx, &updated)
}
