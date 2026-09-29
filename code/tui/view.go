package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/tsengmingchun/deepagents_golang/code/client"
)

var (
	styleUser      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleTool      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleToolError = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleInfo      = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true)
	styleError     = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleStatus    = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("7")).Padding(0, 1)
	styleHelp      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleBox       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("11")).Padding(0, 1)
	styleSelected  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11"))
	styleAdd       = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	styleDel       = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	styleHunk      = lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	styleLLM       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	styleMeta      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func (m *model) View() string {
	parts := []string{m.statusLine(), m.vp.View()}
	if box := m.panel(); box != "" {
		parts = append(parts, box)
	}
	parts = append(parts, m.input.View(), styleHelp.Render(m.helpLine()))
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// layout sizes the viewport around the fixed rows and refreshes its content
// when the transcript changed. It follows new output only if the view was
// already at the bottom, so reading older output is not interrupted.
func (m *model) layout() {
	fixed := 1 + m.input.Height() + 1
	if box := m.panel(); box != "" {
		fixed += lipgloss.Height(box)
	}
	follow := m.vp.AtBottom()
	m.vp.Width = m.width
	m.vp.Height = max(m.height-fixed, 3)
	key := contentKey{gen: m.gen, version: m.version, entries: len(m.entries), width: m.width, height: m.vp.Height, streaming: m.streaming.Len()}
	if key == m.shown {
		return
	}
	if key.width != m.shown.width || key.entries != m.shown.entries || key.streaming != m.shown.streaming ||
		key.gen != m.shown.gen || key.version != m.shown.version {
		m.vp.SetContent(m.transcript())
	}
	if follow || key.gen != m.shown.gen {
		m.vp.GotoBottom()
	}
	m.shown = key
}

// contentKey identifies what the viewport currently shows. Entries change
// by appending, by an in-place update that bumps version, or by a reset
// that bumps gen.
type contentKey struct {
	gen, version, entries, width, height, streaming int
}

func (m *model) statusLine() string {
	parts := []string{"dcode", filepath.Base(m.opts.Cwd), "thread " + m.thread}
	if m.opts.ModelName != "" {
		parts = append(parts, m.opts.ModelName)
	}
	if m.tokens > 0 {
		parts = append(parts, formatTokens(m.tokens)+" tokens")
	}
	if m.auto {
		parts = append(parts, "AUTO-APPROVE")
	}
	if m.mode == modeRunning {
		parts = append(parts, m.spin.View()+" working")
	}
	return styleStatus.Width(m.width).Render(strings.Join(parts, " · "))
}

func (m *model) helpLine() string {
	switch m.mode {
	case modeRunning:
		return "Ctrl-C cancel run · PgUp/PgDn scroll"
	case modeApprove:
		return "a approve · r reject · A approve all · Ctrl-C reject all"
	case modeReason:
		return "Enter reject with reason · Esc back"
	case modePicker:
		return "↑/↓ choose · Enter open · Esc close"
	default:
		return "Enter send · /help commands · Ctrl-C quit · PgUp/PgDn scroll"
	}
}

// transcript renders each finished entry once per width and keeps the
// joined result; only the streaming tail and entries updated in place are
// rendered again.
func (m *model) transcript() string {
	width := max(m.width-2, 20)
	wrap := lipgloss.NewStyle().Width(width)
	c := &m.cache
	if c.gen != m.gen || c.width != width || len(c.rendered) > len(m.entries) {
		*c = transcriptCache{gen: m.gen, width: width}
	}
	rebuild := false
	for i := range m.entries {
		if i == len(c.rendered) {
			c.rendered, c.valid = append(c.rendered, ""), append(c.valid, false)
		}
		if c.valid[i] {
			continue
		}
		c.rendered[i] = m.renderEntry(m.entries[i], wrap, width) + "\n"
		c.valid[i] = true
		m.renders++
		rebuild = rebuild || i < c.joined
	}
	if rebuild {
		c.prefix.Reset()
		c.joined = 0
	}
	for ; c.joined < len(c.rendered); c.joined++ {
		c.prefix.WriteString(c.rendered[c.joined])
	}
	out := c.prefix.String()
	if s := m.streaming.String(); s != "" {
		out += wrap.Render(s) + "\n"
	}
	return strings.TrimRight(out, "\n")
}

type transcriptCache struct {
	gen, width int
	rendered   []string
	valid      []bool
	// joined counts the rendered entries already written to prefix.
	joined int
	prefix strings.Builder
}

func (m *model) renderEntry(e entry, wrap lipgloss.Style, width int) string {
	switch e.kind {
	case entryUser:
		return "\n" + styleUser.Inherit(wrap).Render("> "+e.text)
	case entryAssistant:
		return m.markdown(e.text, width)
	case entryLLM:
		return renderLLM(e, width)
	case entryTool:
		return renderTool(e, width)
	case entryBatch:
		return styleMeta.Render("     " + e.text)
	case entryToolError:
		return styleToolError.Inherit(wrap).Render("✗ " + e.text)
	case entryInfo:
		return styleInfo.Inherit(wrap).Render(e.text)
	case entryError:
		return styleError.Inherit(wrap).Render("error: " + e.text)
	default:
		return wrap.Render(e.text)
	}
}

// markdown renders a finished assistant message, falling back to plain
// wrapped text if glamour fails.
func (m *model) markdown(text string, width int) string {
	if m.md == nil || m.mdWidth != width {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(m.mdStyle), glamour.WithWordWrap(width))
		if err != nil {
			return lipgloss.NewStyle().Width(width).Render(text)
		}
		m.md, m.mdWidth = r, width
	}
	out, err := m.md.Render(text)
	if err != nil {
		return lipgloss.NewStyle().Width(width).Render(text)
	}
	return strings.Trim(out, "\n")
}

// preview is the current action's diff or preview, computed once per action
// because a diff reads the file from disk.
func (m *model) preview() string {
	idx := len(m.decisions)
	if m.previewIdx != idx {
		m.previewText = colorDiff(client.ActionPreviewIn(m.current(), m.opts.Cwd))
		m.previewIdx = idx
	}
	return m.previewText
}

func colorDiff(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			lines[i] = styleTool.Render(l)
		case strings.HasPrefix(l, "@@"):
			lines[i] = styleHunk.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = styleAdd.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = styleDel.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// clipLines keeps the first n lines of s, noting how many were dropped.
func clipLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n") + styleHelp.Render(fmt.Sprintf("\n... %d more lines (enlarge the window to see them)", len(lines)-n))
}

// panel is the approval box or thread picker shown above the input.
func (m *model) panel() string {
	width := max(m.width-4, 20)
	switch m.mode {
	case modeApprove, modeReason:
		req := m.current()
		var b strings.Builder
		fmt.Fprintf(&b, "Approve %d/%d: %s", len(m.decisions)+1, len(m.pending), client.ActionTitle(req))
		if preview := m.preview(); preview != "" {
			// Leave room for the status bar, input, help, title, and a few transcript lines.
			b.WriteString("\n\n" + clipLines(preview, max(m.height-16, 4)))
		}
		return styleBox.Width(width).Render(b.String())
	case modePicker:
		var b strings.Builder
		b.WriteString("Threads in this directory\n")
		for i, t := range m.threads {
			line := fmt.Sprintf("%s  %s  %3d msgs  %s", t.ThreadID, t.UpdatedAt.Local().Format("01-02 15:04"), t.MessageCount, t.Title)
			if i == m.cursor {
				line = styleSelected.Render("› " + line)
			} else {
				line = "  " + line
			}
			b.WriteString("\n" + line)
		}
		return styleBox.Width(width).Render(b.String())
	}
	return ""
}
