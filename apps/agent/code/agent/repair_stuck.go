package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	core "github.com/tsengmingchun/quill/apps/agent/agent"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	rt "github.com/tsengmingchun/quill/apps/agent/runtime"
	"github.com/tsengmingchun/quill/apps/agent/tool"
)

const (
	// stuckMarker starts the reset note the detector appends to a tool
	// result, followed by a JSON report.
	stuckMarker        = "[stuck]"
	stuckRefusalPrefix = stuckMarker + " Edit not applied"
	// stuckAttempts is how many attempts in a row without progress count
	// as stuck.
	stuckAttempts = 3
	// stuckAlternatingCalls is how many calls switching between the same
	// two count as going in circles.
	stuckAlternatingCalls = 8
)

// Rules the detector reports.
const (
	stuckSameFailure = "same_failure"
	stuckNoProgress  = "no_progress"
	stuckSamePatch   = "same_patch"
	stuckAlternating = "alternating"
	// stuckEnvironment: the checks keep failing before they reach the
	// code, so the advice is about the environment, not the hypothesis.
	stuckEnvironment = "environment"
)

// stuckReport is why the detector asked for a new strategy. Resets counts
// the reports in the current turn, this one included.
type stuckReport struct {
	Rule   string `json:"rule"`
	Detail string `json:"detail"`
	Resets int    `json:"resets"`
}

// StuckDetector notices when the repair loop goes in circles and asks for
// a different strategy: the same failure after 3 attempts, 3 attempts that
// neither fix nor reduce the failure, an edit repeated after a check
// rejected it, or calls alternating between the same two without an edit.
// Checks that failed because of the environment never reached the code:
// they do not count for those rules, and 3 in a row ask for the
// environment to be fixed instead.
// The reset note goes on the tool result that showed it. Only what comes
// after the last note counts, so each note needs fresh evidence.
//
// With Enforce (headless runs), the first edit after a note is refused
// once unless the model recorded a different hypothesis first.
type StuckDetector struct {
	Enforce bool
}

func (StuckDetector) Name() string { return "StuckDetectorMiddleware" }

func (d StuckDetector) WrapToolCall(ctx context.Context, req *core.ToolCallRequest, next core.ToolHandler) (*tool.Result, error) {
	var history []llm.Message
	if req.State != nil {
		history = req.State.Messages
	}
	if d.Enforce && mutatingTools[req.ToolCall.Name] {
		if msg := stuckRefusal(history); msg != "" {
			return &tool.Result{Content: []llm.ContentBlock{llm.TextBlock(msg)}, IsError: true}, nil
		}
	}
	res, err := next(ctx, req)
	if err != nil || res == nil {
		return res, err
	}
	m := llm.NewToolMessage(req.ToolCall.ID, req.ToolCall.Name, resultText(res))
	if res.IsError {
		m.Status = llm.ToolStatusError
	}
	msgs := append(history[:len(history):len(history)], m)
	if r, ok := detectStuck(msgs); ok {
		checkpoint := ""
		if saved := checkpoints(msgs, userTurnStart(msgs)); len(saved) > 0 {
			last := saved[len(saved)-1]
			checkpoint = fmt.Sprintf("%s (%s) with the %s tool", last.ID, last.Reason, RollbackToolName)
		}
		res.Content = append(slices.Clone(res.Content), llm.TextBlock(stuckNote(r, checkpoint)))
	}
	return res, nil
}

// stuckNote is the reset note for r. checkpoint, when set, describes a
// checkpoint to roll back to.
func stuckNote(r stuckReport, checkpoint string) string {
	raw, _ := json.Marshal(r)
	var b strings.Builder
	fmt.Fprintf(&b, "\n\n%s %s\n", stuckMarker, raw)
	if r.Rule == stuckEnvironment {
		b.WriteString("Stop editing the code: the checks fail before they reach it, so no edit can change the result. " +
			"Fix the environment (see the [repair] advice), or ask the user if you cannot. " +
			"If it stays broken, finish and say in your final answer which changes are unverified.")
		return b.String()
	}
	b.WriteString("Stop patching and change strategy: more edits on the same idea will not help. ")
	b.WriteString("Before the next edit, call repair set_hypothesis with a different cause. Ways to find it:\n")
	b.WriteString("- re-localize: read the callers and callees on the failing path, not only the code you have been changing\n")
	b.WriteString("- read the history: git log -p on the files or functions involved\n")
	b.WriteString("- print the actual values with a small script or a debug test\n")
	b.WriteString("- ask the explorer subagent for an independent read of the failing path\n")
	if checkpoint != "" {
		fmt.Fprintf(&b, "- roll back to %s and try another approach from there\n", checkpoint)
	}
	return strings.TrimRight(b.String(), "\n")
}

// detectStuck reads the current turn after the last reset note.
func detectStuck(msgs []llm.Message) (stuckReport, bool) {
	start := userTurnStart(msgs)
	reset, resets, _ := lastStuckNote(msgs, start)
	from := start
	if reset >= 0 {
		from = reset + 1
	}
	report := func(rule, detail string) (stuckReport, bool) {
		return stuckReport{Rule: rule, Detail: detail, Resets: resets + 1}, true
	}
	if detail := repeatedPatch(msgs[from:]); detail != "" {
		return report(stuckSamePatch, detail)
	}
	var recent, ranCode []Attempt
	for _, a := range deriveRepair(msgs).Attempts {
		if a.at < from {
			continue
		}
		recent = append(recent, a)
		if a.Result.Category != FailureEnvironment {
			ranCode = append(ranCode, a)
		}
	}
	if len(recent) >= stuckAttempts {
		last := recent[len(recent)-stuckAttempts:]
		if !slices.ContainsFunc(last, func(a Attempt) bool { return a.Result.Category != FailureEnvironment }) {
			return report(stuckEnvironment, fmt.Sprintf("the last %d checks could not run because of the environment (%s)", stuckAttempts, last[len(last)-1].Result.Env))
		}
	}
	if len(ranCode) >= stuckAttempts {
		last := ranCode[len(ranCode)-stuckAttempts:]
		// A check that keeps passing is "unchanged" too, but not a failure.
		if last[len(last)-1].Result.Category != FailurePassed &&
			!slices.ContainsFunc(last, func(a Attempt) bool { return a.Delta == nil || a.Delta.Kind != DeltaUnchanged }) {
			return report(stuckSameFailure, fmt.Sprintf("the last %d attempts all left the same failure: %s", stuckAttempts, failureLabel(*last[len(last)-1].Result)))
		}
		if !slices.ContainsFunc(last, madeProgress) {
			return report(stuckNoProgress, fmt.Sprintf("%d attempts in a row neither fixed nor reduced the failure (now: %s)", stuckAttempts, failureLabel(*last[len(last)-1].Result)))
		}
	}
	if a, b, ok := alternatingCalls(msgs[from:]); ok {
		return report(stuckAlternating, fmt.Sprintf("the last %d calls alternate between %s and %s without an edit", stuckAlternatingCalls, describeCall(a), describeCall(b)))
	}
	return stuckReport{}, false
}

func madeProgress(a Attempt) bool {
	return a.Result.Category == FailurePassed || a.Delta != nil && (a.Delta.Kind == DeltaFixed || a.Delta.Kind == DeltaProgressed)
}

func failureLabel(s Signature) string {
	if len(s.Tests) > 0 {
		return strings.Join(clipList(s.Tests, 3), ", ")
	}
	if len(s.Details) > 0 {
		return string(s.Category) + ": " + clipLine(s.Details[0], 120)
	}
	return string(s.Category)
}

func describeCall(c llm.ToolCall) string {
	for _, k := range []string{"file_path", "pattern", "command", "path"} {
		if v, ok := c.Args[k].(string); ok && v != "" {
			return fmt.Sprintf("%s %s", c.Name, clipLine(v, 60))
		}
	}
	return c.Name
}

// repeatedPatch describes a successful edit identical to an earlier one
// that a failing check followed, or returns "".
func repeatedPatch(msgs []llm.Message) string {
	rejected := map[string]bool{}
	var unchecked []string
	calls := map[string]llm.ToolCall{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok {
			continue
		}
		text := m.Text()
		failed := m.Status == llm.ToolStatusError || strings.HasPrefix(text, "Error")
		if mutatingTools[c.Name] {
			if failed {
				continue
			}
			key := callKey(c)
			if rejected[key] {
				return fmt.Sprintf("%s on %s repeats an edit that a failing check already rejected", c.Name, strings.Join(editedFiles(c), ", "))
			}
			unchecked = append(unchecked, key)
			continue
		}
		if s, ok := ClassifyFailure(c, text); ok && s.Category != FailureEnvironment {
			for _, k := range unchecked {
				rejected[k] = s.Category != FailurePassed
			}
			unchecked = nil
		}
	}
	return ""
}

// alternatingCalls reports whether the last stuckAlternatingCalls calls
// since the last successful edit switch between the same two calls.
func alternatingCalls(msgs []llm.Message) (llm.ToolCall, llm.ToolCall, bool) {
	var seq []llm.ToolCall
	calls := map[string]llm.ToolCall{}
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok {
			continue
		}
		if changesFiles(c.Name) && m.Status != llm.ToolStatusError && !strings.HasPrefix(m.Text(), "Error") {
			seq = nil
			continue
		}
		seq = append(seq, c)
	}
	if len(seq) < stuckAlternatingCalls {
		return llm.ToolCall{}, llm.ToolCall{}, false
	}
	seq = seq[len(seq)-stuckAlternatingCalls:]
	if callKey(seq[0]) == callKey(seq[1]) {
		return llm.ToolCall{}, llm.ToolCall{}, false
	}
	for i := 2; i < len(seq); i++ {
		if callKey(seq[i]) != callKey(seq[i-2]) {
			return llm.ToolCall{}, llm.ToolCall{}, false
		}
	}
	return seq[0], seq[1], true
}

// lastStuckNote finds the last reset note in msgs[start:]: its index (-1
// when there is none), how many there are and the report it carries.
func lastStuckNote(msgs []llm.Message, start int) (int, int, stuckReport) {
	at, count := -1, 0
	var last stuckReport
	note := "\n\n" + stuckMarker + " "
	for i := start; i < len(msgs); i++ {
		if msgs[i].Role != llm.RoleTool {
			continue
		}
		text := msgs[i].Text()
		j := strings.Index(text, note)
		if j < 0 {
			continue
		}
		line, _, _ := strings.Cut(text[j+len(note):], "\n")
		var r stuckReport
		if json.Unmarshal([]byte(line), &r) != nil {
			continue
		}
		at, count, last = i, count+1, r
	}
	return at, count, last
}

// stuckOpen returns the last reset report of the current turn when the
// model has neither recorded a different hypothesis nor edited since.
func stuckOpen(msgs []llm.Message) (stuckReport, bool) {
	start := userTurnStart(msgs)
	reset, _, r := lastStuckNote(msgs, start)
	if reset < 0 || changedCourse(msgs, reset, true) {
		return stuckReport{}, false
	}
	return r, true
}

// stuckRefusal returns the error to send instead of an edit after a reset
// note, or "" to let it run: a different hypothesis since the note, or an
// earlier refusal, lets edits through.
func stuckRefusal(history []llm.Message) string {
	start := userTurnStart(history)
	reset, _, r := lastStuckNote(history, start)
	if reset < 0 || r.Rule == stuckEnvironment || changedCourse(history, reset, false) {
		return ""
	}
	return fmt.Sprintf("%s: you are stuck (%s: %s) and have not changed the hypothesis since. "+
		"Call repair set_hypothesis with a different cause first (the current one: %q). "+
		"This edit is refused once; send it again only if you are sure it is right.",
		stuckRefusalPrefix, r.Rule, r.Detail, hypothesisAt(history, reset))
}

// changedCourse reports whether, after msgs[reset], the model recorded a
// hypothesis other than the one in force at the reset, had an edit
// refused, or (with edits) applied an edit.
func changedCourse(msgs []llm.Message, reset int, edits bool) bool {
	before := hypothesisAt(msgs, reset)
	calls := map[string]llm.ToolCall{}
	for i, m := range msgs {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if i <= reset || m.Role != llm.RoleTool || !ok {
			continue
		}
		text := m.Text()
		failed := m.Status == llm.ToolStatusError || strings.HasPrefix(text, "Error")
		switch {
		case strings.HasPrefix(text, stuckRefusalPrefix):
			return true
		case c.Name == RepairToolName && !failed:
			if h, _ := c.Args["hypothesis"].(string); strings.TrimSpace(h) != "" && strings.TrimSpace(h) != before {
				return true
			}
		case edits && changesFiles(c.Name) && !failed:
			return true
		}
	}
	return false
}

// hypothesisAt is the last hypothesis recorded with the repair tool up to
// msgs[at].
func hypothesisAt(msgs []llm.Message, at int) string {
	h := ""
	calls := map[string]llm.ToolCall{}
	for _, m := range msgs[:at+1] {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		c, ok := calls[m.ToolCallID]
		if m.Role != llm.RoleTool || !ok || c.Name != RepairToolName || m.Status == llm.ToolStatusError || strings.HasPrefix(m.Text(), "Error") {
			continue
		}
		if s, _ := c.Args["hypothesis"].(string); strings.TrimSpace(s) != "" {
			h = strings.TrimSpace(s)
		}
	}
	return h
}

// userTurnStart is the index of the last real user message.
func userTurnStart(msgs []llm.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if rt.IsUserTurn(msgs[i]) {
			return i
		}
	}
	return 0
}
