package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/subagents"
)

// subColors tells concurrent subagents apart; they are assigned in turn.
var subColors = []lipgloss.Color{"6", "3", "5", "4", "2", "14"}

// subRun is the activity of one subagent started by a task call. It is drawn
// beneath its task row, which is the entry at index entry.
type subRun struct {
	num       int
	agentType string
	desc      string
	style     lipgloss.Style
	entry     int
	// rows are the subagent's model-call and tool rows.
	rows     []entry
	llmCalls int
	pending  map[string]bool
	// replied is set once the subagent answers without tool calls.
	replied       bool
	thinkingSince time.Time
	done          bool
	report        string
	// background runs report through a task notification, not the task result.
	background bool
}

func (m *model) newSubRun(call llm.ToolCall, entryIdx int, started time.Time) *subRun {
	m.subSeq++
	typ, _ := call.Args["subagent_type"].(string)
	desc, _ := call.Args["description"].(string)
	background, _ := call.Args["run_in_background"].(bool)
	s := &subRun{
		num:           m.subSeq,
		agentType:     typ,
		desc:          firstLine(desc),
		style:         lipgloss.NewStyle().Bold(true).Foreground(subColors[(m.subSeq-1)%len(subColors)]),
		entry:         entryIdx,
		pending:       map[string]bool{},
		thinkingSince: started,
		background:    background,
	}
	m.subs[call.ID] = s
	return s
}

func (s *subRun) label() string { return fmt.Sprintf("task #%d", s.num) }

func (s *subRun) title() string {
	return fmt.Sprintf("%s %s · %s", s.label(), s.agentType, s.desc)
}

// thinking reports whether the subagent is waiting for its model.
func (s *subRun) thinking() bool {
	return !s.done && !s.background && !s.replied && len(s.pending) == 0
}

// finish records the task result, which ends the subagent run unless it
// started in the background.
func (s *subRun) finish(msg llm.Message) {
	if msg.Status == llm.ToolStatusError {
		s.done = true
		return
	}
	if s.background {
		s.report = "running in the background"
		return
	}
	s.done = true
	s.report = firstLine(msg.Text())
}

// taskNotification finishes the background run a notification reports on.
func (m *model) taskNotification(n subagents.Notification) {
	s := m.subs[n.TaskID]
	if s == nil {
		m.info(fmt.Sprintf("background task %s %s", n.TaskID, n.Status))
		return
	}
	s.done, s.report = true, firstLine(n.Result)
	e := &m.entries[s.entry]
	mark := "✓"
	if n.Status != subagents.TaskCompleted {
		mark, e.failed = "✗ "+n.Status, true
	}
	e.meta = mark
	m.invalidate(s.entry)
}

// stop ends a run that will not report back (cancelled or failed run).
func (s *subRun) stop(label string) {
	s.done = true
	for i := range s.rows {
		if s.rows[i].running {
			s.rows[i].running, s.rows[i].meta = false, label
		}
	}
}

// subMessage adds a message of the subagent run named by ns to its task row.
// Deeper namespaces (a subagent's own subagents) count as the top-level run.
func (m *model) subMessage(ns []string, msg llm.Message) {
	id, ok := strings.CutPrefix(ns[0], subagents.NamespacePrefix)
	s := m.subs[id]
	if !ok || s == nil || s.done {
		return
	}
	switch msg.Role {
	case llm.RoleAssistant:
		s.llmCalls++
		m.addUsage(msg)
		s.rows = append(s.rows, entry{kind: entryLLM, text: fmt.Sprintf("#%d · LLM %d", s.num, s.llmCalls), meta: llmMeta(msg)})
		s.replied = len(msg.ToolCalls) == 0
		started := callsStarted(msg)
		for i, call := range msg.ToolCalls {
			s.pending[call.ID] = true
			s.rows = append(s.rows, toolRow(call, i == len(msg.ToolCalls)-1, started))
		}
	case llm.RoleTool:
		for i := len(s.rows) - 1; i >= 0; i-- {
			if s.rows[i].kind == entryTool && s.rows[i].callID == msg.ToolCallID {
				settle(&s.rows[i], msg)
				break
			}
		}
		delete(s.pending, msg.ToolCallID)
		if len(s.pending) == 0 {
			s.thinkingSince = time.Now()
		}
	default:
		return
	}
	m.invalidate(s.entry)
}

// runningSubs counts subagents that have not finished.
func (m *model) runningSubs() int {
	n := 0
	for _, s := range m.subs {
		if !s.done {
			n++
		}
	}
	return n
}

// renderSub draws a subagent's rows under its task row, indented by prefix.
func (m *model) renderSub(s *subRun, prefix string, width int) []string {
	guide := styleTool.Render(prefix)
	var out []string
	for _, r := range s.rows {
		switch r.kind {
		case entryLLM:
			out = append(out, withMeta(guide+s.style.Render("◆ "+r.text), r.meta, width))
		case entryTool:
			out = append(out, m.renderTool(r, prefix, width))
		}
	}
	if s.thinking() {
		row := guide + s.style.Render(fmt.Sprintf("◆ #%d · LLM %d", s.num, s.llmCalls+1)) + styleInfo.Render("  thinking")
		out = append(out, withMeta(row, m.liveMeta(s.thinkingSince), width))
	}
	if s.report != "" {
		room := max(width-lipgloss.Width(prefix)-2, 10)
		out = append(out, styleMeta.Render(prefix+ansi.Truncate("↳ "+s.report, room, "…")))
	}
	return out
}

func isNotification(msg llm.Message) bool {
	_, ok := subagents.ParseNotification(msg.Text())
	return ok
}
