package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/tsengmingchun/deepagents_golang/code/askuser"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/hitl"
	"github.com/tsengmingchun/deepagents_golang/middleware/planmode"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
)

// renderTodos draws a todo list as a checklist with the first line of
// each item's notes beneath it.
func renderTodos(title string, todos []todolist.Todo) string {
	var b strings.Builder
	done := 0
	for _, t := range todos {
		if t.Status == todolist.StatusCompleted {
			done++
		}
	}
	fmt.Fprintf(&b, "%s %d/%d", title, done, len(todos))
	for _, t := range todos {
		mark := "○"
		switch t.Status {
		case todolist.StatusInProgress:
			mark = "▸"
		case todolist.StatusCompleted:
			mark = "✓"
		case todolist.StatusCancelled:
			mark = "⊘"
		}
		fmt.Fprintf(&b, "\n  %s %s", mark, t.Content)
		if t.Notes != "" {
			fmt.Fprintf(&b, "\n      %s", firstLine(t.Notes))
		}
	}
	return b.String()
}

// todoChecklist renders a write_todos call from its arguments; a merge call
// only carries the changed items until its result brings the full list.
func todoChecklist(call llm.ToolCall) (string, bool) {
	if call.Name != todolist.ToolName {
		return "", false
	}
	todos, err := todolist.ParseTodos(call.Args["todos"])
	if err != nil {
		return "", false
	}
	title := "todos"
	if merge, _ := call.Args["merge"].(bool); merge {
		title = "todos (update)"
	}
	return renderTodos(title, todos), true
}

// planRow renders a create_plan call: title, overview and checklist.
func planRow(call llm.ToolCall) (string, bool) {
	if call.Name != planmode.ToolName {
		return "", false
	}
	name, _ := call.Args["name"].(string)
	overview, _ := call.Args["overview"].(string)
	var todos []todolist.Todo
	if list, ok := call.Args["todos"].([]any); ok {
		for _, it := range list {
			item, _ := it.(map[string]any)
			content, _ := item["content"].(string)
			todos = append(todos, todolist.Todo{Content: content, Status: todolist.StatusPending})
		}
	}
	text := renderTodos("plan · "+name+" · todos", todos)
	if overview != "" {
		text = strings.Replace(text, "\n", "\n  "+firstLine(overview)+"\n", 1)
	}
	return text, true
}

// planMarkdown is the plan body shown in the transcript for review.
func planMarkdown(call llm.ToolCall) string {
	name, _ := call.Args["name"].(string)
	overview, _ := call.Args["overview"].(string)
	body, _ := call.Args["plan"].(string)
	return fmt.Sprintf("# %s\n\n%s\n\n%s", name, overview, body)
}

// toolResultText updates a write_todos or create_plan row once its result
// arrives and returns entries to add after it (the plan for review).
func toolResultText(e *entry, msg llm.Message) []entry {
	if msg.Status == llm.ToolStatusError {
		return nil
	}
	switch msg.Name {
	case todolist.ToolName:
		if todos, ok := todolist.ParseResult(msg.Text()); ok {
			e.text = renderTodos("todos", todos)
		}
	case planmode.ToolName:
		e.text += "\n  " + firstLine(msg.Text())
		return []entry{
			{kind: entryAssistant, text: planMarkdown(e.call)},
			{kind: entryInfo, text: "plan ready — review it above; /build implements it, or keep chatting in plan mode to revise"},
		}
	}
	return nil
}

// askState walks through the questions of one ask_user call.
type askState struct {
	qs      []askuser.Question
	answers []askuser.Answer
	// q indexes the current question; cursor its highlighted option, where
	// len(options) is "Other".
	q, cursor int
	picked    map[int]bool
	// other is set while the user types a free-form answer in the input.
	other bool
}

func (m *model) enterAsk() tea.Cmd {
	qs, err := askuser.ParseQuestions(m.current().Args)
	if err != nil {
		m.decide(hitl.Decision{Type: hitl.DecisionReject, Message: err.Error()})
		return m.awaitDecision()
	}
	m.ask = &askState{qs: qs, picked: map[int]bool{}}
	m.mode = modeAsk
	return nil
}

func (a *askState) question() askuser.Question { return a.qs[a.q] }

// next records answer and moves on; it reports whether all are answered.
func (a *askState) next(answer askuser.Answer) bool {
	a.answers = append(a.answers, answer)
	a.q, a.cursor, a.picked, a.other = a.q+1, 0, map[int]bool{}, false
	return a.q >= len(a.qs)
}

func (m *model) askKey(k tea.KeyMsg) tea.Cmd {
	a := m.ask
	q := a.question()
	if a.other {
		switch k.Type {
		case tea.KeyEsc:
			a.other = false
			m.input.Reset()
			m.resetPlaceholder()
			return nil
		case tea.KeyEnter:
			text := strings.TrimSpace(m.input.Value())
			m.input.Reset()
			m.resetPlaceholder()
			answer := askuser.Answer{QuestionID: q.ID, Other: text}
			if q.AllowMultiple {
				answer.OptionIDs = a.pickedIDs(q)
			}
			return m.answered(answer)
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		return cmd
	}
	switch s := k.String(); {
	case k.Type == tea.KeyEsc || k.Type == tea.KeyCtrlC:
		m.ask = nil
		m.decide(hitl.Decision{Type: hitl.DecisionReject, Message: "The user skipped these questions; proceed with your best judgment and state your assumptions."})
		return m.awaitDecision()
	case s == "up" || s == "k":
		a.cursor = max(a.cursor-1, 0)
	case s == "down" || s == "j":
		a.cursor = min(a.cursor+1, len(q.Options))
	case len(s) == 1 && s >= "1" && s <= "9":
		if n := int(s[0] - '1'); n < len(q.Options) {
			a.cursor = n
		}
	case s == " " && q.AllowMultiple && a.cursor < len(q.Options):
		a.picked[a.cursor] = !a.picked[a.cursor]
	case k.Type == tea.KeyEnter:
		if a.cursor == len(q.Options) {
			a.other = true
			m.input.Reset()
			m.input.Placeholder = "Your answer (Enter to submit, Esc to go back)"
			return nil
		}
		answer := askuser.Answer{QuestionID: q.ID, OptionIDs: []string{q.Options[a.cursor].ID}}
		if q.AllowMultiple && len(a.picked) > 0 {
			answer.OptionIDs = a.pickedIDs(q)
		}
		return m.answered(answer)
	}
	return nil
}

func (a *askState) pickedIDs(q askuser.Question) []string {
	var ids []string
	for i, o := range q.Options {
		if a.picked[i] {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func (m *model) answered(answer askuser.Answer) tea.Cmd {
	a := m.ask
	if !a.next(answer) {
		return nil
	}
	m.ask = nil
	m.decide(hitl.Decision{Type: hitl.DecisionRespond, Message: askuser.FormatAnswers(a.qs, a.answers)})
	return m.awaitDecision()
}

// askPanel shows the current question and its options.
func (m *model) askPanel() string {
	a := m.ask
	q := a.question()
	var b strings.Builder
	fmt.Fprintf(&b, "Question %d/%d: %s", a.q+1, len(a.qs), q.Prompt)
	if q.AllowMultiple {
		b.WriteString(styleHelp.Render("  (several allowed)"))
	}
	labels := make([]string, 0, len(q.Options)+1)
	for _, o := range q.Options {
		labels = append(labels, o.Label)
	}
	labels = append(labels, "Other (type your own answer)")
	for i, label := range labels {
		box := ""
		if q.AllowMultiple && i < len(q.Options) {
			box = "[ ] "
			if a.picked[i] {
				box = "[x] "
			}
		}
		line := fmt.Sprintf("%d. %s%s", i+1, box, label)
		if i == a.cursor {
			line = styleSelected.Render("› " + line)
		} else {
			line = "  " + line
		}
		b.WriteString("\n" + line)
	}
	return b.String()
}
