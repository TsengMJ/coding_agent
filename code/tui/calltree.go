package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tsengmingchun/deepagents_golang/code/client"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
)

// The transcript shows each model call as a top-level row and the tool
// calls it requested as a second level beneath it. A task call nests the
// subagent's own model and tool calls, numbered and colored per subagent.
// Running rows show a spinner and the time elapsed so far:
//
//	◆ main · LLM #2                         1.2k in / 230 out · 2.4s
//	  (assistant text)
//	  ├─ read_file /README.md                                   12ms
//	  └─ ◇ task #1 general-purpose · find the handlers        ⠼ 8.3s
//	       ◆ #1 · LLM 1                      800 in / 90 out · 1.1s
//	         └─ grep "HandleFunc"                            ⠼ 0.4s
//	◆ main · LLM #3  thinking                                ⠼ 1.2s

// toolBatch tracks the tool calls of one model reply until all have results.
type toolBatch struct {
	pending map[string]bool
	timings []llm.Timing
	calls   int
}

// assistant adds the model-call row, its text, and one row per tool call.
func (m *model) assistant(msg llm.Message, text string) {
	m.llmCalls++
	m.addUsage(msg)
	m.entries = append(m.entries, entry{kind: entryLLM, text: fmt.Sprintf("main · LLM #%d", m.llmCalls), meta: llmMeta(msg)})
	if text != "" {
		m.entries = append(m.entries, entry{kind: entryAssistant, text: text})
	}
	m.replied = len(msg.ToolCalls) == 0
	if m.replied {
		return
	}
	m.batch = &toolBatch{pending: map[string]bool{}, calls: len(msg.ToolCalls)}
	started := callsStarted(msg)
	for i, call := range msg.ToolCalls {
		e := toolRow(call, i == len(msg.ToolCalls)-1, started)
		if call.Name == subagents.TaskToolName {
			e.sub = m.newSubRun(call, len(m.entries), started)
			e.text = e.sub.title()
		}
		m.batch.pending[call.ID] = true
		m.live[len(m.entries)] = true
		m.entries = append(m.entries, e)
	}
}

func (m *model) addUsage(msg llm.Message) {
	if msg.Usage != nil {
		m.tokens += msg.Usage.TotalTokens
	}
}

// callsStarted estimates when the tool calls of msg started: when its model
// call ended, or now if the message is untimed.
func callsStarted(msg llm.Message) time.Time {
	if t, ok := llm.TimingOf(msg); ok {
		return t.End()
	}
	return time.Now()
}

// toolRow is the running row of one tool call.
func toolRow(call llm.ToolCall, last bool, started time.Time) entry {
	text, ok := todoChecklist(call)
	if !ok {
		text, ok = planRow(call)
	}
	if !ok {
		text = strings.TrimPrefix(client.ToolCallLine(call), "[tool] ")
	}
	return entry{kind: entryTool, text: text, callID: call.ID, last: last, running: true, started: started, call: call}
}

// settle fills in the duration and error of a tool row from its result.
func settle(e *entry, msg llm.Message) (llm.Timing, bool) {
	timing, timed := llm.TimingOf(msg)
	e.running, e.meta = false, ""
	if timed {
		e.meta = formatDuration(timing.Duration)
	}
	if msg.Status == llm.ToolStatusError {
		e.failed = true
		e.text += " — " + firstLine(msg.Text())
	}
	return timing, timed
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
	if idx < 0 {
		if msg.Status == llm.ToolStatusError {
			m.entries = append(m.entries, entry{kind: entryToolError, text: msg.Name + ": " + firstLine(msg.Text())})
		}
		return
	}
	e := &m.entries[idx]
	timing, timed := settle(e, msg)
	extra := toolResultText(e, msg)
	if e.sub != nil {
		e.sub.finish(msg)
		mark := " ✓"
		switch {
		case e.failed:
			mark = " ✗"
		case !e.sub.done:
			mark = " ⇢ background"
		}
		e.meta = strings.TrimSpace(e.meta + mark)
	}
	delete(m.live, idx)
	m.invalidate(idx)
	m.finishBatchCall(msg.ToolCallID, timing, timed)
	m.entries = append(m.entries, extra...)
}

// settleLive ends every running row with label when the run stops without
// their results (cancelled or failed).
func (m *model) settleLive(label string) {
	for i := range m.live {
		e := &m.entries[i]
		e.running, e.meta = false, label
		if e.sub != nil {
			e.sub.stop(label)
		}
		m.invalidate(i)
	}
	for _, s := range m.subs {
		if s.background && !s.done {
			s.stop(label)
			m.invalidate(s.entry)
		}
	}
	clear(m.live)
	m.batch = nil
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
	m.thinkingSince = time.Now()
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

// formatElapsed shows the running time of a live row, in tenths of a second
// so it visibly advances.
func formatElapsed(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", max(d, 0).Seconds())
	}
	return formatDuration(d)
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

// liveMeta is the spinner and elapsed time of something still running; it
// only moves while a run is streaming.
func (m *model) liveMeta(since time.Time) string {
	if m.mode != modeRunning {
		return "…"
	}
	return strings.TrimRight(m.spin.View(), " ") + " " + formatElapsed(time.Since(since))
}

// thinkingRow is the live row of a main-agent model call in flight, shown
// below the transcript while the run waits for the model.
func (m *model) thinkingRow(width int) string {
	if m.mode != modeRunning || m.batch != nil || m.replied {
		return ""
	}
	row := styleLLM.Render(fmt.Sprintf("◆ main · LLM #%d", m.llmCalls+1)) + styleInfo.Render("  thinking")
	return "\n" + withMeta(row, m.liveMeta(m.thinkingSince), width)
}

// renderTool draws a tool row under prefix; extra lines (a todo checklist)
// continue under the tree guide, and a task row is followed by its
// subagent's rows.
func (m *model) renderTool(e entry, prefix string, width int) string {
	branch, cont := prefix+"  ├─ ", prefix+"  │  "
	if e.last {
		branch, cont = prefix+"  └─ ", prefix+"     "
	}
	style := styleTool
	if e.failed {
		style = styleToolError
	}
	meta := e.meta
	if e.running {
		meta = m.liveMeta(e.started)
	}
	lines := strings.Split(e.text, "\n")
	room := width - lipgloss.Width(branch) - lipgloss.Width(meta) - 2
	var first string
	if e.sub != nil {
		label := "◇ " + e.sub.label()
		rest := strings.TrimPrefix(lines[0], e.sub.label())
		first = styleTool.Render(branch) + e.sub.style.Render(label) + style.Render(ansi.Truncate(rest, max(room-lipgloss.Width(label), 10), "…"))
	} else {
		first = style.Render(branch + ansi.Truncate(lines[0], max(room, 10), "…"))
	}
	out := []string{withMeta(first, meta, width)}
	for _, l := range lines[1:] {
		out = append(out, style.Render(cont+ansi.Truncate(l, max(width-len(cont), 10), "…")))
	}
	if e.sub != nil {
		out = append(out, m.renderSub(e.sub, cont+"  ", width)...)
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
