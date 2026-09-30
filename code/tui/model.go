// Package tui is the interactive terminal UI built on Bubble Tea
// (Python Textual app: `app.py`, `tui/*`).
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/tsengmingchun/deepagents_golang/code/client"
	"github.com/tsengmingchun/deepagents_golang/code/commands"
	"github.com/tsengmingchun/deepagents_golang/code/server"
	"github.com/tsengmingchun/deepagents_golang/code/sessions"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
)

// Options configures the TUI.
type Options struct {
	Server server.Server
	Cwd    string
	// ThreadID continues a thread; empty starts a new one.
	ThreadID    string
	AutoApprove bool
	// ModelName names the server default model, shown in the status bar
	// until a thread or /model says otherwise.
	ModelName string
	// Model runs every message on this "provider:model" until /model
	// changes it; empty lets the server choose.
	Model string
	// SaveDefaultModel backs `/model --default`.
	SaveDefaultModel func(spec string) error
	// MarkdownStyle is a glamour standard style; Run picks "dark" or
	// "light" from the terminal background when empty.
	MarkdownStyle string
}

// Run shows the TUI until the user quits or ctx is cancelled.
func Run(ctx context.Context, opts Options) error {
	if opts.Server == nil || opts.Cwd == "" {
		return errors.New("tui.Run: Server and Cwd are required")
	}
	if opts.MarkdownStyle == "" {
		// Ask the terminal before Bubble Tea owns stdin.
		opts.MarkdownStyle = "light"
		if lipgloss.HasDarkBackground() {
			opts.MarkdownStyle = "dark"
		}
	}
	m := newModel(ctx, opts)
	defer m.stopRun()
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil
	}
	return err
}

type mode int

const (
	modeIdle mode = iota
	modeRunning
	modeApprove
	modeReason
	modePicker
)

type entryKind int

const (
	entryUser entryKind = iota
	entryAssistant
	entryTool
	entryToolError
	entryInfo
	entryError
	// entryLLM is one model call; tool rows below it are its second level.
	entryLLM
	// entryBatch summarizes the timing of a finished multi-call batch.
	entryBatch
)

type entry struct {
	kind entryKind
	text string
	// meta is shown right-aligned: tokens and duration.
	meta string
	// callID, last and failed describe a tool row.
	callID string
	last   bool
	failed bool
	// running marks a tool row still waiting for its result since started.
	running bool
	started time.Time
	// sub is the subagent run of a task row.
	sub *subRun
}

// Messages delivered to Update.
type (
	eventMsg        struct{ ev server.Event }
	runEndMsg       struct{ err error }
	threadLoadedMsg struct {
		id  string
		ts  *server.ThreadState
		err error
	}
	threadsMsg struct {
		list []sessions.Info
		err  error
	}
	compactMsg struct {
		res *server.CompactResult
		err error
	}
)

type model struct {
	ctx    context.Context
	srv    server.Server
	opts   Options
	thread string
	auto   bool
	model  client.ModelChoice

	entries   []entry
	streaming strings.Builder
	mode      mode
	cancel    context.CancelFunc
	events    chan tea.Msg
	cancelled bool

	pending   []hitl.ActionRequest
	decisions []hitl.Decision

	threads []sessions.Info
	cursor  int

	vp            viewport.Model
	input         textarea.Model
	spin          spinner.Model
	width, height int

	// gen changes whenever entries is replaced rather than appended to;
	// version changes whenever an existing entry is updated in place.
	gen     int
	version int

	llmCalls int
	tokens   int
	batch    *toolBatch
	// replied is set when the main agent answered without tool calls, so no
	// model call is in flight until the next run or tool results.
	replied       bool
	thinkingSince time.Time
	// live holds the indices of entries with running rows, re-rendered on
	// every spinner tick.
	live    map[int]bool
	subs    map[string]*subRun
	subSeq  int
	cache   transcriptCache
	shown   contentKey
	renders int

	// mdStyle is a glamour standard style ("dark", "light", "notty").
	mdStyle string
	md      *glamour.TermRenderer
	mdWidth int

	previewIdx  int
	previewText string

	cmds *commands.Registry
	// queued collects the background work slash command handlers start.
	queued []tea.Cmd
}

func newModel(ctx context.Context, opts Options) *model {
	in := textarea.New()
	in.Placeholder = "Ask dcode to write code... (Enter to send, /help for commands)"
	in.ShowLineNumbers = false
	in.SetHeight(3)
	in.CharLimit = 0
	in.KeyMap.InsertNewline.SetEnabled(false)
	in.Focus()
	m := &model{
		ctx:        ctx,
		srv:        opts.Server,
		opts:       opts,
		thread:     opts.ThreadID,
		auto:       opts.AutoApprove,
		model:      client.ModelChoice{Override: opts.Model, Default: opts.ModelName},
		vp:         viewport.New(80, 20),
		input:      in,
		spin:       spinner.New(spinner.WithSpinner(spinner.Dot)),
		width:      80,
		height:     24,
		mdStyle:    opts.MarkdownStyle,
		previewIdx: -1,
		live:       map[int]bool{},
		subs:       map[string]*subRun{},
	}
	if m.mdStyle == "" {
		m.mdStyle = "dark"
	}
	m.cmds = client.NewCommandRegistry(tuiSession{m}, client.CommandOptions{
		HelpFooter:       "keys: Enter send · Ctrl-C cancel run / quit · PgUp/PgDn scroll",
		SaveDefaultModel: opts.SaveDefaultModel,
	})
	if m.thread == "" {
		m.thread = sessions.NewThreadID()
		m.info("new thread " + m.thread)
	}
	return m
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}
	if m.opts.ThreadID != "" {
		cmds = append(cmds, m.loadThread(m.opts.ThreadID))
	}
	return tea.Batch(cmds...)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.update(msg)
	m.layout()
	return m, cmd
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width <= 0 || msg.Height <= 0 {
			return nil
		}
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(msg.Width)
		return nil
	case tea.KeyMsg:
		return m.key(msg)
	case eventMsg:
		m.event(msg.ev)
		return m.waitEvent()
	case runEndMsg:
		return m.runEnded(msg.err)
	case threadLoadedMsg:
		m.threadLoaded(msg)
		return nil
	case threadsMsg:
		if msg.err != nil {
			m.errorf("list threads: %v", msg.err)
			return nil
		}
		m.threads, m.cursor, m.mode = msg.list, 0, modePicker
		if len(msg.list) == 0 {
			m.info("no threads in this directory")
			m.mode = modeIdle
		}
		return nil
	case compactMsg:
		if msg.err != nil {
			m.errorf("compact: %v", msg.err)
			return nil
		}
		m.info(client.CompactSummary(msg.res))
		return nil
	case spinner.TickMsg:
		if m.mode != modeRunning {
			return nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		for i := range m.live {
			m.invalidate(i)
		}
		// Also redraws the thinking row, which is not cached.
		m.version++
		return cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

func (m *model) key(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyPgUp, tea.KeyPgDown:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return cmd
	}
	switch m.mode {
	case modeApprove:
		return m.approveKey(k)
	case modeReason:
		return m.reasonKey(k)
	case modePicker:
		return m.pickerKey(k)
	case modeRunning:
		if k.Type == tea.KeyCtrlC {
			m.stopRun()
			return nil
		}
		if k.Type == tea.KeyEnter {
			return nil
		}
	case modeIdle:
		switch k.Type {
		case tea.KeyCtrlD:
			return tea.Quit
		case tea.KeyCtrlC:
			if m.input.Value() == "" {
				return tea.Quit
			}
			m.input.Reset()
			return nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			return m.submit(text)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return cmd
}

func (m *model) submit(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return m.command(text)
	}
	m.entries = append(m.entries, entry{kind: entryUser, text: text})
	return m.startRun(server.RunRequest{Messages: []llm.Message{llm.NewUserMessage(text)}})
}

func (m *model) command(line string) tea.Cmd {
	m.queued = nil
	if err := m.cmds.Dispatch(m.ctx, line); err != nil {
		m.errorf("%v", err)
	}
	cmds := m.queued
	m.queued = nil
	return tea.Batch(cmds...)
}

// tuiSession runs slash commands against the TUI model; slow work is
// queued as tea.Cmds and reported back through Update.
type tuiSession struct{ m *model }

func (s tuiSession) Thread() string { return s.m.thread }

func (s tuiSession) NewThread() {
	s.m.thread = sessions.NewThreadID()
	s.m.model.Thread = ""
	s.m.resetEntries()
	s.m.info("new thread " + s.m.thread)
}

func (s tuiSession) ListThreads(context.Context) {
	srv, cwd, ctx := s.m.srv, s.m.opts.Cwd, s.m.ctx
	s.m.queued = append(s.m.queued, func() tea.Msg {
		list, err := srv.ListThreads(ctx, sessions.Filter{Cwd: cwd, Limit: 20})
		return threadsMsg{list: list, err: err}
	})
}

// Resume adopts the thread's remembered model, dropping any /model choice.
func (s tuiSession) Resume(_ context.Context, id string) {
	s.m.model.Override = ""
	s.m.queued = append(s.m.queued, s.m.loadThread(id))
}

func (s tuiSession) Compact(context.Context) {
	srv, thread, ctx := s.m.srv, s.m.thread, s.m.ctx
	s.m.info("compacting older messages…")
	s.m.queued = append(s.m.queued, func() tea.Msg {
		res, err := srv.Compact(ctx, thread)
		return compactMsg{res: res, err: err}
	})
}

func (s tuiSession) AutoApprove() bool { return s.m.auto }

func (s tuiSession) SetAutoApprove(on bool) {
	s.m.auto = on
	if on {
		s.m.info("auto-approve on: tool calls run without asking")
	} else {
		s.m.info("auto-approve off")
	}
}

func (s tuiSession) Model() string { return s.m.model.Current() }

func (s tuiSession) SetModel(spec string) { s.m.model.Override = spec }

func (s tuiSession) Quit() { s.m.queued = append(s.m.queued, tea.Quit) }

func (s tuiSession) Info(text string) { s.m.info(text) }

// startRun streams req in a goroutine; events come back through waitEvent.
func (m *model) startRun(req server.RunRequest) tea.Cmd {
	req.ThreadID, req.Cwd, req.AutoApprove, req.Interactive = m.thread, m.opts.Cwd, m.auto, true
	req.Model = m.model.Override
	ctx, cancel := context.WithCancel(m.ctx)
	ch := make(chan tea.Msg, 64)
	srv := m.srv
	go func() {
		defer close(ch)
		send := func(msg tea.Msg) bool {
			select {
			case ch <- msg:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for ev, err := range srv.Run(ctx, req) {
			if err != nil {
				send(runEndMsg{err: err})
				return
			}
			if !send(eventMsg{ev: ev}) {
				return
			}
		}
		send(runEndMsg{})
	}()
	m.cancel, m.events, m.mode, m.cancelled = cancel, ch, modeRunning, false
	m.pending, m.decisions = nil, nil
	m.replied, m.thinkingSince = false, time.Now()
	return tea.Batch(m.waitEvent(), m.spin.Tick)
}

func (m *model) waitEvent() tea.Cmd {
	ch := m.events
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return runEndMsg{err: context.Canceled}
		}
		return msg
	}
}

func (m *model) stopRun() {
	if m.cancel != nil {
		m.cancelled = true
		m.cancel()
	}
}

func (m *model) event(ev server.Event) {
	if len(ev.Namespace) > 0 {
		// Subagent text is not shown; its model and tool calls nest under
		// the task row.
		if ev.Kind == server.EventMessage {
			m.subMessage(ev.Namespace, *ev.Message)
		}
		return
	}
	switch ev.Kind {
	case server.EventDelta:
		m.streaming.WriteString(ev.Text)
	case server.EventMessage:
		m.message(*ev.Message)
	case server.EventInterrupt:
		if len(ev.Interrupts) > 0 {
			m.pending = ev.Interrupts[0].ActionRequests
		}
	}
}

func (m *model) message(msg llm.Message) {
	switch msg.Role {
	case llm.RoleAssistant:
		text := strings.TrimSpace(msg.Text())
		if text == "" {
			text = strings.TrimSpace(m.streaming.String())
		}
		m.streaming.Reset()
		m.assistant(msg, text)
	case llm.RoleTool:
		m.toolResult(msg)
	}
}

func (m *model) runEnded(err error) tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	cancelled := m.cancelled
	m.cancel, m.events, m.cancelled = nil, nil, false
	if text := strings.TrimSpace(m.streaming.String()); text != "" {
		m.entries = append(m.entries, entry{kind: entryAssistant, text: text})
	}
	m.streaming.Reset()
	switch {
	case cancelled:
		m.info("cancelled")
	case err != nil && !errors.Is(err, context.Canceled):
		m.errorf("%v", err)
	}
	if len(m.pending) > 0 && !cancelled && err == nil {
		m.mode, m.decisions, m.previewIdx = modeApprove, nil, -1
		return nil
	}
	switch {
	case cancelled:
		m.settleLive("cancelled")
	case err != nil:
		m.settleLive("✗")
	default:
		m.settleLive("")
	}
	m.pending = nil
	m.mode = modeIdle
	return nil
}

func (m *model) current() hitl.ActionRequest { return m.pending[len(m.decisions)] }

func (m *model) approveKey(k tea.KeyMsg) tea.Cmd {
	switch {
	case k.Type == tea.KeyCtrlC:
		for len(m.decisions) < len(m.pending) {
			m.decide(hitl.Decision{Type: hitl.DecisionReject, Message: "User cancelled this tool call."})
		}
		return m.finishApproval()
	case k.String() == "a" || k.String() == "y":
		m.decide(hitl.Decision{Type: hitl.DecisionApprove})
	case k.String() == "A":
		for len(m.decisions) < len(m.pending) {
			m.decide(hitl.Decision{Type: hitl.DecisionApprove})
		}
	case k.String() == "r" || k.String() == "n":
		m.mode = modeReason
		m.input.Reset()
		m.input.Placeholder = "Why reject? (Enter to reject, empty for no reason, Esc to go back)"
		return nil
	default:
		return nil
	}
	return m.finishApproval()
}

func (m *model) reasonKey(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEsc:
		m.mode = modeApprove
		m.input.Reset()
		m.resetPlaceholder()
		return nil
	case tea.KeyEnter:
		reason := strings.TrimSpace(m.input.Value())
		m.input.Reset()
		m.resetPlaceholder()
		m.mode = modeApprove
		m.decide(hitl.Decision{Type: hitl.DecisionReject, Message: reason})
		return m.finishApproval()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return cmd
}

func (m *model) resetPlaceholder() {
	m.input.Placeholder = "Ask dcode to write code... (Enter to send, /help for commands)"
}

func (m *model) decide(d hitl.Decision) {
	req := m.current()
	title := client.ActionTitle(req)
	if d.Type == hitl.DecisionApprove {
		m.entries = append(m.entries, entry{kind: entryInfo, text: "approved " + title})
	} else {
		text := "rejected " + title
		if d.Message != "" {
			text += ": " + d.Message
		}
		m.entries = append(m.entries, entry{kind: entryInfo, text: text})
	}
	m.decisions = append(m.decisions, d)
}

// finishApproval resumes the thread once every pending call has a decision.
func (m *model) finishApproval() tea.Cmd {
	if len(m.decisions) < len(m.pending) {
		return nil
	}
	decisions := m.decisions
	return m.startRun(server.RunRequest{Resume: &server.Resume{Decisions: decisions}})
}

func (m *model) pickerKey(k tea.KeyMsg) tea.Cmd {
	if k.Type == tea.KeyEsc || k.Type == tea.KeyCtrlC {
		m.mode = modeIdle
		return nil
	}
	switch k.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.threads)-1 {
			m.cursor++
		}
	case "enter":
		id := m.threads[m.cursor].ThreadID
		m.mode = modeIdle
		m.model.Override = ""
		return m.loadThread(id)
	case "q":
		m.mode = modeIdle
	}
	return nil
}

func (m *model) loadThread(id string) tea.Cmd {
	srv, ctx := m.srv, m.ctx
	return func() tea.Msg {
		ts, err := srv.GetThread(ctx, id)
		return threadLoadedMsg{id: id, ts: ts, err: err}
	}
}

func (m *model) threadLoaded(msg threadLoadedMsg) {
	switch {
	case msg.err != nil:
		m.errorf("load thread %s: %v", msg.id, msg.err)
		return
	case msg.ts == nil:
		m.errorf("thread %s not found", msg.id)
		return
	}
	m.thread = msg.id
	m.model.Thread = msg.ts.Model
	m.resetEntries()
	for _, msg := range msg.ts.Messages {
		if msg.Role == llm.RoleUser {
			m.entries = append(m.entries, entry{kind: entryUser, text: msg.Text()})
			continue
		}
		m.message(msg)
	}
	m.info(fmt.Sprintf("resumed thread %s (%d messages)", msg.id, len(msg.ts.Messages)))
	if len(msg.ts.Interrupts) > 0 {
		m.pending, m.decisions, m.mode, m.previewIdx = msg.ts.Interrupts[0].ActionRequests, nil, modeApprove, -1
		return
	}
	// Calls without results belong to a run that ended early.
	m.settleLive("")
}

func (m *model) resetEntries() {
	m.entries = nil
	m.gen++
	m.llmCalls, m.tokens, m.batch, m.replied = 0, 0, nil, false
	m.live, m.subs, m.subSeq = map[int]bool{}, map[string]*subRun{}, 0
}

// invalidate marks entry i as changed so the transcript re-renders it.
func (m *model) invalidate(i int) {
	m.version++
	if i < len(m.cache.valid) {
		m.cache.valid[i] = false
	}
}

func (m *model) info(text string) { m.entries = append(m.entries, entry{kind: entryInfo, text: text}) }

func (m *model) errorf(format string, args ...any) {
	m.entries = append(m.entries, entry{kind: entryError, text: fmt.Sprintf(format, args...)})
}

// todoChecklist renders a write_todos call as a checklist instead of its raw
// JSON arguments.
func todoChecklist(call llm.ToolCall) (string, bool) {
	if call.Name != todolist.ToolName {
		return "", false
	}
	todos, err := todolist.ParseTodos(call.Args["todos"])
	if err != nil {
		return "", false
	}
	var b strings.Builder
	b.WriteString("todos")
	for _, t := range todos {
		mark := "○"
		switch t.Status {
		case todolist.StatusInProgress:
			mark = "▸"
		case todolist.StatusCompleted:
			mark = "✓"
		}
		fmt.Fprintf(&b, "\n  %s %s", mark, t.Content)
	}
	return b.String(), true
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}
