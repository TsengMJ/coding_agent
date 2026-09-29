package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tsengmingchun/deepagents_golang/code/client"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// The transcript shows each model call as a top-level row and the tool
// calls it requested as a second level beneath it:
//
//	◆ LLM #2                                1.2k in / 230 out · 2.4s
//	  (assistant text)
//	  ├─ read_file /README.md                                   12ms
//	  └─ execute ls                                            340ms
//	     ⇉ 2 tools · wall 341ms · sum 352ms · parallel

// toolBatch tracks the tool calls of one model reply until all have results.
type toolBatch struct {
	pending map[string]bool
	timings []llm.Timing
	calls   int
}

// assistant adds the model-call row, its text, and one row per tool call.
func (m *model) assistant(msg llm.Message, text string) {
	m.llmCalls++
	if msg.Usage != nil {
		m.tokens += msg.Usage.TotalTokens
	}
	m.entries = append(m.entries, entry{kind: entryLLM, text: fmt.Sprintf("LLM #%d", m.llmCalls), meta: llmMeta(msg)})
	if text != "" {
		m.entries = append(m.entries, entry{kind: entryAssistant, text: text})
	}
	if len(msg.ToolCalls) == 0 {
		return
	}
	m.batch = &toolBatch{pending: map[string]bool{}, calls: len(msg.ToolCalls)}
	for i, call := range msg.ToolCalls {
		text, ok := todoChecklist(call)
		if !ok {
			text = strings.TrimPrefix(client.ToolCallLine(call), "[tool] ")
		}
		m.batch.pending[call.ID] = true
		m.entries = append(m.entries, entry{kind: entryTool, text: text, callID: call.ID, last: i == len(msg.ToolCalls)-1, meta: "…"})
	}
}

// toolResult fills in the duration (and error) of the row for msg's call.
func (m *model) toolResult(msg llm.Message) {
	idx := -1
	for i := len(m.entries) - 1; i >= 0; i-- {
		if m.entries[i].kind == entryTool && m.entries[i].callID == msg.ToolCallID {
			idx = i
			break
		}
	}
	failed := msg.Status == llm.ToolStatusError
	if idx < 0 {
		if failed {
			m.entries = append(m.entries, entry{kind: entryToolError, text: msg.Name + ": " + firstLine(msg.Text())})
		}
		return
	}
	e := &m.entries[idx]
	timing, timed := llm.TimingOf(msg)
	e.meta = ""
	if timed {
		e.meta = formatDuration(timing.Duration)
	}
	if failed {
		e.failed = true
		e.text += " — " + firstLine(msg.Text())
	}
	m.invalidate(idx)
	m.finishBatchCall(msg.ToolCallID, timing, timed)
}

// finishBatchCall records one result; once every call of a multi-call batch
// is back, it adds a summary comparing wall time to the sum of durations,
// which shows whether the calls overlapped.
func (m *model) finishBatchCall(id string, t llm.Timing, timed bool) {
	b := m.batch
	if b == nil || !b.pending[id] {
		return
	}
	delete(b.pending, id)
	if timed {
		b.timings = append(b.timings, t)
	}
	if len(b.pending) > 0 {
		return
	}
	m.batch = nil
	if b.calls < 2 || len(b.timings) != b.calls {
		return
	}
	m.entries = append(m.entries, entry{kind: entryBatch, text: batchSummary(b.timings)})
}

func batchSummary(ts []llm.Timing) string {
	start, end := ts[0].Start, ts[0].End()
	var sum time.Duration
	for _, t := range ts {
		if t.Start.Before(start) {
			start = t.Start
		}
		if t.End().After(end) {
			end = t.End()
		}
		sum += t.Duration
	}
	wall := end.Sub(start)
	verdict := "sequential"
	if wall < sum*4/5 {
		verdict = "parallel"
	}
	return fmt.Sprintf("⇉ %d tools · wall %s · sum %s · %s", len(ts), formatDuration(wall), formatDuration(sum), verdict)
}

func llmMeta(msg llm.Message) string {
	var parts []string
	if u := msg.Usage; u != nil {
		s := formatTokens(u.InputTokens) + " in / " + formatTokens(u.OutputTokens) + " out"
		if u.CacheRead > 0 {
			s += " (" + formatTokens(u.CacheRead) + " cached)"
		}
		parts = append(parts, s)
	}
	if t, ok := llm.TimingOf(msg); ok {
		parts = append(parts, formatDuration(t.Duration))
	}
	return strings.Join(parts, " · ")
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// renderLLM draws the model-call row with its metadata right-aligned.
func renderLLM(e entry, width int) string {
	return "\n" + withMeta(styleLLM.Render("◆ "+e.text), e.meta, width)
}

// renderTool draws a second-level tool row; extra lines (a todo checklist)
// continue under the tree guide.
func renderTool(e entry, width int) string {
	branch, cont := "  ├─ ", "  │  "
	if e.last {
		branch, cont = "  └─ ", "     "
	}
	style := styleTool
	if e.failed {
		style = styleToolError
	}
	lines := strings.Split(e.text, "\n")
	room := width - lipgloss.Width(branch) - lipgloss.Width(e.meta) - 2
	first := style.Render(branch + ansi.Truncate(lines[0], max(room, 10), "…"))
	out := []string{withMeta(first, e.meta, width)}
	for _, l := range lines[1:] {
		out = append(out, style.Render(cont+ansi.Truncate(l, max(width-len(cont), 10), "…")))
	}
	return strings.Join(out, "\n")
}

func withMeta(left, meta string, width int) string {
	if meta == "" {
		return left
	}
	gap := max(width-lipgloss.Width(left)-lipgloss.Width(meta), 2)
	return left + strings.Repeat(" ", gap) + styleMeta.Render(meta)
}
