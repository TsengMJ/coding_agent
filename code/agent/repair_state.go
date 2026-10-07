package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
	"github.com/tsengmingchun/quill/tool"
)

const (
	// RepairToolName records the task, suspects and hypothesis of a repair.
	RepairToolName = "repair"
	// RepairStateKey holds the RepairRecord in the agent state.
	RepairStateKey = "repair"
	// RepairStateSource marks the request-only message carrying the repair state.
	RepairStateSource = "repair_state"
	// maxRepairNotes and maxShownAttempts keep the repair state short.
	maxRepairNotes   = 5
	maxShownAttempts = 5
)

// taskKinds are the kinds set_task accepts.
var taskKinds = []string{"bug", "feature", "refactor", "other"}

// RepairRecord is what the model writes with the repair tool. Attempts,
// the baseline and the phase are not stored: deriveRepair recomputes them
// from the conversation.
type RepairRecord struct {
	Task       *TaskSpec    `json:"task,omitempty"`
	Suspects   []Suspect    `json:"suspects,omitempty"`
	Hypothesis string       `json:"hypothesis,omitempty"`
	Intent     *PatchIntent `json:"patch_intent,omitempty"`
	Notes      []string     `json:"notes,omitempty"`
}

// TaskSpec is the request turned into something that can be checked.
type TaskSpec struct {
	// Kind is bug, feature, refactor or other; only a bug has a failure to
	// reproduce before editing.
	Kind         string   `json:"kind,omitempty"`
	Expected     []string `json:"expected"`
	Constraints  []string `json:"constraints,omitempty"`
	Verification []string `json:"verification,omitempty"`
}

// Suspect is a place the cause probably is.
type Suspect struct {
	File       string  `json:"file"`
	Symbol     string  `json:"symbol,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// PatchIntent is the change the model expects to make for its hypothesis.
type PatchIntent struct {
	Files []string `json:"files,omitempty"`
	Scope string   `json:"scope,omitempty"`
}

func (r RepairRecord) empty() bool {
	return r.Task == nil && len(r.Suspects) == 0 && r.Hypothesis == "" && r.Intent == nil && len(r.Notes) == 0
}

// parseRepairRecord reads a state value, which is a RepairRecord in memory
// and a JSON object after a checkpoint round trip.
func parseRepairRecord(v any) (RepairRecord, error) {
	switch r := v.(type) {
	case nil:
		return RepairRecord{}, nil
	case RepairRecord:
		return r, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return RepairRecord{}, err
	}
	var r RepairRecord
	if err := json.Unmarshal(raw, &r); err != nil {
		return RepairRecord{}, fmt.Errorf("repair state: %w", err)
	}
	return r, nil
}

// reduceRepair merges a partial update into the record: set fields replace,
// notes append (keeping the last maxRepairNotes). Parallel repair calls
// therefore both land.
func reduceRepair(current, update any) (any, error) {
	cur, err := parseRepairRecord(current)
	if err != nil {
		return nil, err
	}
	up, err := parseRepairRecord(update)
	if err != nil {
		return nil, err
	}
	if up.Task != nil {
		cur.Task = up.Task
	}
	if up.Suspects != nil {
		cur.Suspects = up.Suspects
	}
	if up.Hypothesis != "" {
		cur.Hypothesis = up.Hypothesis
		cur.Intent = up.Intent
	}
	if len(up.Notes) > 0 {
		cur.Notes = append(slices.Clone(cur.Notes), up.Notes...)
		if len(cur.Notes) > maxRepairNotes {
			cur.Notes = cur.Notes[len(cur.Notes)-maxRepairNotes:]
		}
	}
	return cur, nil
}

// RepairPhase is where the current turn is in the repair loop.
type RepairPhase string

const (
	// PhaseUnderstand: no check has run and nothing was edited yet.
	PhaseUnderstand RepairPhase = "understand"
	// PhaseReproduced: a check failed before any edit (the baseline).
	PhaseReproduced RepairPhase = "reproduced"
	// PhasePatching: files were edited since the last check.
	PhasePatching RepairPhase = "patching"
	// PhaseDiagnosing: the check after the last edit failed.
	PhaseDiagnosing RepairPhase = "diagnosing"
	// PhaseVerified: the check after the last edit passed.
	PhaseVerified RepairPhase = "verified"
)

// Attempt is one round of edits and the check that followed them.
type Attempt struct {
	Hypothesis string
	Files      []string
	Result     *Signature
	Delta      *Delta
	// at is the index of the check's result in the conversation.
	at int
}

// repairView is the repair state derived from the current turn.
type repairView struct {
	Phase    RepairPhase
	Baseline *Signature
	Attempts []Attempt
	// Open is the attempt whose edits have not been checked yet.
	Open *Attempt
	// Files are every file the turn edited, relative to the root.
	Files []string
	// Plan, Reached and Pending describe the verification of Files, when a
	// planner is set.
	Plan    VerificationPlan
	Reached VerifyLevel
	Pending []PlannedCheck
	// Stuck is the last reset note the model has not acted on yet.
	Stuck *stuckReport
	// Checkpoints are the checkpoints saved in the current turn.
	Checkpoints []checkpointNote
	Next        string
}

// deriveRepair reads the current turn: the first failing check before any
// edit is the baseline; each run of successful edits followed by a check
// is an attempt, compared with the previous run of the same tests.
func deriveRepair(msgs []llm.Message) repairView {
	start := userTurnStart(msgs)
	var v repairView
	hypothesis := ""
	edited := false
	calls := map[string]llm.ToolCall{}
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
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
		failed := m.Status == llm.ToolStatusError || strings.HasPrefix(text, "Error")
		switch {
		case c.Name == RepairToolName:
			if h, _ := c.Args["hypothesis"].(string); !failed && strings.TrimSpace(h) != "" {
				hypothesis = strings.TrimSpace(h)
			}
		case changesFiles(c.Name):
			if failed {
				continue
			}
			edited = true
			if v.Open == nil {
				v.Open = &Attempt{Hypothesis: hypothesis}
			}
			for _, f := range editedFiles(c) {
				if !slices.Contains(v.Open.Files, f) {
					v.Open.Files = append(v.Open.Files, f)
				}
				if rel := strings.TrimPrefix(path.Clean("/"+f), "/"); !slices.Contains(v.Files, rel) {
					v.Files = append(v.Files, rel)
				}
			}
		default:
			s, ok := ClassifyFailure(c, text)
			if !ok {
				continue
			}
			if v.Open == nil {
				if !edited && v.Baseline == nil && s.Category != FailurePassed {
					v.Baseline = &s
				}
				continue
			}
			v.Open.Result = &s
			v.Open.at = i
			if prev, ok := previousSignature(msgs[start:i], c, s); ok {
				d := CompareFailures(prev, s)
				v.Open.Delta = &d
			}
			v.Attempts = append(v.Attempts, *v.Open)
			v.Open = nil
		}
	}
	v.Phase, v.Next = repairPhase(v)
	v.Checkpoints = checkpoints(msgs, start)
	if r, ok := stuckOpen(msgs); ok {
		v.Stuck = &r
		v.Next = fmt.Sprintf("You are stuck (%s): call repair set_hypothesis with a different cause before editing again.", r.Rule)
	}
	return v
}

func repairPhase(v repairView) (RepairPhase, string) {
	switch {
	case v.Open != nil:
		if v.Baseline != nil {
			return PhasePatching, fmt.Sprintf("Rerun `%s` to see whether the edit fixed the baseline failure.", v.Baseline.Check)
		}
		return PhasePatching, "Run the relevant tests to check the edit."
	case len(v.Attempts) > 0:
		last := v.Attempts[len(v.Attempts)-1]
		if last.Result.Category == FailurePassed {
			return PhaseVerified, "The targeted check passes: widen verification (the changed package, then its dependents) and review git_diff before finishing."
		}
		if last.Delta == nil {
			return PhaseDiagnosing, "Read the failure before the next edit."
		}
		switch last.Delta.Kind {
		case DeltaUnchanged:
			return PhaseDiagnosing, "The last edit did not change the failure: its cause is elsewhere. Revisit the hypothesis (repair set_hypothesis) before editing again."
		case DeltaRegressed:
			return PhaseDiagnosing, "The last edit made things worse: inspect the new failures or undo the edit."
		}
		return PhaseDiagnosing, "The failure changed: refine the hypothesis from the remaining failures."
	case v.Baseline != nil:
		return PhaseReproduced, "The failure is reproduced: state a hypothesis (repair set_hypothesis) before editing."
	}
	return PhaseUnderstand, ""
}

// renderRepairState formats the record and the derived view for the
// request. Strings are JSON-quoted, so the block is valid YAML.
func renderRepairState(rec RepairRecord, v repairView) string {
	var b strings.Builder
	q := func(s string) string {
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	list := func(name string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&b, "  %s:\n", name)
		for _, it := range items {
			fmt.Fprintf(&b, "    - %s\n", q(it))
		}
	}
	b.WriteString("<repair-state>\n")
	fmt.Fprintf(&b, "phase: %s\n", v.Phase)
	if rec.Task != nil {
		b.WriteString("task:\n")
		if rec.Task.Kind != "" {
			fmt.Fprintf(&b, "  kind: %s\n", rec.Task.Kind)
		}
		list("expected", rec.Task.Expected)
		list("constraints", rec.Task.Constraints)
		list("verification", rec.Task.Verification)
	}
	if len(rec.Suspects) > 0 {
		b.WriteString("suspects:\n")
		for _, s := range rec.Suspects {
			fmt.Fprintf(&b, "  - {file: %s", q(s.File))
			if s.Symbol != "" {
				fmt.Fprintf(&b, ", symbol: %s", q(s.Symbol))
			}
			if s.Confidence > 0 {
				fmt.Fprintf(&b, ", confidence: %.2g", s.Confidence)
			}
			b.WriteString("}\n")
		}
	}
	if rec.Hypothesis != "" {
		fmt.Fprintf(&b, "hypothesis: %s\n", q(rec.Hypothesis))
	}
	if rec.Intent != nil && (len(rec.Intent.Files) > 0 || rec.Intent.Scope != "") {
		files, _ := json.Marshal(rec.Intent.Files)
		fmt.Fprintf(&b, "patch_intent: {files: %s, scope: %s}\n", files, q(rec.Intent.Scope))
	}
	if v.Baseline != nil {
		fmt.Fprintf(&b, "baseline: %s\n", signatureSummary(*v.Baseline))
	}
	if len(v.Attempts) > 0 {
		shown := v.Attempts
		fmt.Fprintf(&b, "attempts: # %d attempts", len(v.Attempts))
		if len(shown) > maxShownAttempts {
			fmt.Fprintf(&b, ", last %d shown", maxShownAttempts)
			shown = shown[len(shown)-maxShownAttempts:]
		}
		b.WriteString("\n")
		first := len(v.Attempts) - len(shown) + 1
		for i, a := range shown {
			files, _ := json.Marshal(a.Files)
			fmt.Fprintf(&b, "  - attempt %d: {files: %s", first+i, files)
			if a.Hypothesis != "" {
				fmt.Fprintf(&b, ", hypothesis: %s", q(clipLine(a.Hypothesis, 160)))
			}
			fmt.Fprintf(&b, ", result: %s", signatureSummary(*a.Result))
			if a.Delta != nil {
				fmt.Fprintf(&b, ", delta: %s", a.Delta.Kind)
				if len(a.Delta.Fixed) > 0 {
					fmt.Fprintf(&b, ", fixed: %s", q(strings.Join(clipList(a.Delta.Fixed, 5), ", ")))
				}
				if len(a.Delta.New) > 0 {
					fmt.Fprintf(&b, ", new: %s", q(strings.Join(clipList(a.Delta.New, 5), ", ")))
				}
			}
			b.WriteString("}\n")
		}
	}
	if v.Open != nil {
		files, _ := json.Marshal(v.Open.Files)
		fmt.Fprintf(&b, "unchecked_edits: %s\n", files)
	}
	if len(v.Checkpoints) > 0 {
		shown := v.Checkpoints
		if len(shown) > maxShownCheckpoints {
			shown = shown[len(shown)-maxShownCheckpoints:]
		}
		var items []string
		for _, c := range shown {
			items = append(items, fmt.Sprintf("{id: %s, reason: %s}", c.ID, q(c.Reason)))
		}
		fmt.Fprintf(&b, "checkpoints: [%s]\n", strings.Join(items, ", "))
	}
	if v.Stuck != nil {
		fmt.Fprintf(&b, "stuck: {rule: %s, resets: %d, detail: %s}\n", v.Stuck.Rule, v.Stuck.Resets, q(clipLine(v.Stuck.Detail, 200)))
	}
	renderVerification(&b, v.Plan, v.Reached, v.Pending)
	if len(rec.Notes) > 0 {
		b.WriteString("notes:\n")
		for _, n := range rec.Notes {
			fmt.Fprintf(&b, "  - %s\n", q(n))
		}
	}
	if v.Next != "" {
		fmt.Fprintf(&b, "next: %s\n", q(v.Next))
	}
	b.WriteString("</repair-state>")
	return b.String()
}

// signatureSummary is a one-line description of a check result.
func signatureSummary(s Signature) string {
	out := string(s.Category)
	if s.Env != "" {
		out += " " + s.Env
	}
	if len(s.Tests) > 0 {
		out += " " + strings.Join(clipList(s.Tests, 5), ", ")
	} else if len(s.Details) > 0 {
		out += " " + clipLine(s.Details[0], 160)
	}
	raw, _ := json.Marshal(out)
	return fmt.Sprintf("{check: %s, %s: %s}", jsonString(s.Check), "result", raw)
}

func jsonString(s string) string {
	raw, _ := json.Marshal(clipLine(s, 120))
	return string(raw)
}

func clipList(items []string, n int) []string {
	if len(items) <= n {
		return items
	}
	return append(slices.Clone(items[:n]), fmt.Sprintf("... and %d more", len(items)-n))
}

// RepairTracker keeps the repair loop's state in front of the model. It
// offers the repair tool, stores what the model records with it, and adds
// the record plus the attempts derived from the current turn (baseline,
// edits, before / after comparison, phase) to each request as a message
// after the conversation, so the system message and history stay a
// cacheable prefix. With a Planner it also shows the verification plan for
// the edited files and how far the checks since the last edit reach.
type RepairTracker struct {
	Planner Planner
}

func (RepairTracker) Name() string { return "RepairTrackerMiddleware" }

func (RepairTracker) StateSchema() rt.Schema {
	return rt.Schema{RepairStateKey: {Name: RepairStateKey, Reducer: reduceRepair, Private: true, OmitFromInput: true}}
}

func (t RepairTracker) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	if req.State == nil {
		return next(ctx, req)
	}
	rec, err := parseRepairRecord(req.State.Values[RepairStateKey])
	if err != nil {
		return nil, err
	}
	v := deriveRepair(req.State.Messages)
	if rec.empty() && v.Phase == PhaseUnderstand {
		return next(ctx, req)
	}
	if t.Planner != nil && len(v.Files) > 0 {
		v.Plan = t.Planner.Plan(ctx, codeFiles(v.Files))
		v.Reached, v.Pending = verificationProgress(req.State.Messages, v.Plan)
		if v.Phase == PhaseVerified && len(v.Pending) > 0 {
			v.Next = fmt.Sprintf("Widen verification: %s (%s).", v.Pending[0].describe(), v.Pending[0].Why)
		}
	}
	tail := llm.NewUserMessage(renderRepairState(rec, v))
	tail.Metadata = map[string]any{rt.SyntheticSourceKey: RepairStateSource}
	updated := *req
	updated.Messages = append(req.Messages[:len(req.Messages):len(req.Messages)], tail)
	return next(ctx, &updated)
}

const repairToolDescription = `Keep a short record while fixing a bug or a failing behavior. It is shown back to you with every step, together with your attempts and how each one changed the test results (recorded automatically).

- set_task, once you understand the request: expected behavior, constraints, and how to verify it (concrete commands or tests). Set it again when the request changes.
- set_suspects, after localizing: the files and symbols most likely involved, with your confidence (0-1).
- set_hypothesis, before each patch: the cause you believe in, the files you expect to change (files) and the scope of the change. Replace it when a check disproves it.
- note: a fact you confirmed and will need later (e.g. "repository returns sql.ErrNoRows").

Skip it for questions and trivial one-step edits.`

func (RepairTracker) Tools() []tool.Tool {
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return []tool.Tool{&tool.Func{
		ToolName:        RepairToolName,
		ToolDescription: repairToolDescription,
		ToolEffect:      tool.ReadOnly,
		ToolSchema: tool.Schema{
			"type": "object",
			"properties": map[string]any{
				"op":           map[string]any{"type": "string", "enum": []string{"set_task", "set_suspects", "set_hypothesis", "note"}},
				"kind":         map[string]any{"type": "string", "enum": taskKinds, "description": "set_task: bug (reproduce the failure before editing), feature, refactor or other."},
				"expected":     strList("set_task: the expected behaviors, each checkable."),
				"constraints":  strList("set_task: what must not change."),
				"verification": strList("set_task: commands or tests that show the task is done."),
				"suspects": map[string]any{"type": "array", "description": "set_suspects: likely locations of the cause.", "items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"file":       map[string]any{"type": "string"},
						"symbol":     map[string]any{"type": "string"},
						"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
					},
					"required":             []string{"file"},
					"additionalProperties": false,
				}},
				"hypothesis": map[string]any{"type": "string", "description": "set_hypothesis: the cause you believe in."},
				"files":      strList("set_hypothesis: files you expect to change."),
				"scope":      map[string]any{"type": "string", "description": "set_hypothesis: the scope of the change, e.g. \"service layer only\"."},
				"note":       map[string]any{"type": "string", "description": "note: a confirmed fact."},
			},
			"required":             []string{"op"},
			"additionalProperties": false,
		},
		Fn: runRepairTool,
	}}
}

func runRepairTool(_ context.Context, r *tool.Runtime, args map[string]any) (*tool.Result, error) {
	op, _ := args["op"].(string)
	var up RepairRecord
	switch op {
	case "set_task":
		task := TaskSpec{Expected: trimmed(stringArgs(args["expected"])), Constraints: trimmed(stringArgs(args["constraints"])), Verification: trimmed(stringArgs(args["verification"]))}
		if len(task.Expected) == 0 {
			return nil, errors.New("set_task needs at least one expected behavior")
		}
		task.Kind, _ = args["kind"].(string)
		if task.Kind != "" && !slices.Contains(taskKinds, task.Kind) {
			return nil, fmt.Errorf("kind must be one of %s, got %q", strings.Join(taskKinds, ", "), task.Kind)
		}
		up.Task = &task
	case "set_suspects":
		raw, err := json.Marshal(args["suspects"])
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &up.Suspects); err != nil || len(up.Suspects) == 0 {
			return nil, errors.New("set_suspects needs a list of {file, symbol, confidence}")
		}
		for i, s := range up.Suspects {
			if strings.TrimSpace(s.File) == "" {
				return nil, fmt.Errorf("suspects[%d].file is required", i)
			}
		}
	case "set_hypothesis":
		up.Hypothesis = strings.TrimSpace(fmt.Sprint(args["hypothesis"]))
		if args["hypothesis"] == nil || up.Hypothesis == "" {
			return nil, errors.New("set_hypothesis needs a hypothesis")
		}
		scope, _ := args["scope"].(string)
		up.Intent = &PatchIntent{Files: trimmed(stringArgs(args["files"])), Scope: strings.TrimSpace(scope)}
	case "note":
		n, _ := args["note"].(string)
		if strings.TrimSpace(n) == "" {
			return nil, errors.New("note needs text")
		}
		up.Notes = []string{strings.TrimSpace(n)}
	default:
		return nil, fmt.Errorf("op must be set_task, set_suspects, set_hypothesis or note, got %q", op)
	}
	phase := PhaseUnderstand
	if r != nil && r.State != nil {
		phase = deriveRepair(r.State.Messages).Phase
	}
	text := fmt.Sprintf(`{"ok":true,"op":%q,"phase":%q}`, op, phase)
	id := ""
	if r != nil {
		id = r.ToolCallID
	}
	return &tool.Result{Command: &rt.Command{Update: &rt.Update{
		Values:   map[string]any{RepairStateKey: up},
		Messages: []llm.Message{llm.NewToolMessage(id, RepairToolName, text)},
	}}}, nil
}

func trimmed(items []string) []string {
	var out []string
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
