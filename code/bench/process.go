package bench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
)

// Process holds the process metrics of one attempt. Attempts recorded
// before these existed have a nil Process: unknown, not zero.
type Process struct {
	// DuplicateToolCalls counts main-agent calls whose name and arguments
	// repeat an earlier call; MaxRepeat is the most times one call was made.
	DuplicateToolCalls int `json:"duplicate_tool_calls"`
	MaxRepeat          int `json:"max_repeat"`
	// ToolOutputChars is the result size per tool, each result counted once.
	ToolOutputChars map[string]int `json:"tool_output_chars"`
	// VerifyBlocks counts the distinct messages the verify gate injected.
	VerifyBlocks int `json:"verify_blocks"`
	// TodoOnlyTurns counts main-agent responses that only called write_todos.
	TodoOnlyTurns int `json:"todo_only_turns"`
	// ToolWallMS sums, per main-agent response, its slowest tool call: the
	// tool time on the critical path when a response's calls run in parallel.
	ToolWallMS int64 `json:"tool_wall_ms"`
	// FirstGoldSrcTouchStep is the main-agent step that first read or edited
	// a non-test, non-doc file of the gold patch; nil when none was touched.
	FirstGoldSrcTouchStep *int `json:"first_gold_src_touch_step,omitempty"`
	// GoldSrcRecall is the share of the gold patch's source files the
	// predicted patch changed; set by the grader.
	GoldSrcRecall *float64 `json:"gold_src_recall,omitempty"`
	// ToolsHash fingerprints the tool schemas the main agent was offered.
	ToolsHash string `json:"tools_hash,omitempty"`
	// BaselineVerdicts are the verify gate's HEAD baseline results, in
	// order; set by the solver, since verdicts that let the turn finish
	// leave nothing in the trace.
	BaselineVerdicts []BaselineVerdict `json:"baseline_verdicts,omitempty"`
}

// BaselineVerdict is one HEAD baseline result and the tests it was about.
type BaselineVerdict struct {
	Verdict string   `json:"verdict"`
	Tests   []string `json:"tests,omitempty"`
}

var patchFileLine = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File:\s*(.+?)\s*$`)

// ProcessFromTrace derives the process metrics from the recorded model
// calls. Steps are numbered as in MetricsFromTrace.
func ProcessFromTrace(calls []trace.LLMCallDetail, goldPaths []string) *Process {
	p := &Process{ToolOutputChars: map[string]int{}}
	goldSrc := map[string]bool{}
	for _, g := range GoldSourcePaths(goldPaths) {
		goldSrc[g] = true
	}
	seenArgs := map[string]int{}
	toolNames := map[string]string{}
	callResponse := map[string]int{}
	slowest := map[int]int64{}
	toolSeen := map[string]bool{}
	verifies := map[string]bool{}
	steps, responses := 0, 0
	for _, c := range calls {
		main := len(c.Namespace) == 0
		if main && p.ToolsHash == "" && len(c.Tools) > 0 {
			p.ToolsHash = hashJSON(c.Tools)
		}
		for _, msg := range c.Messages {
			switch {
			case main && msg.Role == llm.RoleUser && strings.HasPrefix(msg.Text(), "[verify]"):
				verifies[msg.Text()] = true
			case msg.Role == llm.RoleTool && msg.ToolCallID != "" && !toolSeen[msg.ToolCallID]:
				toolSeen[msg.ToolCallID] = true
				p.ToolOutputChars[toolNames[msg.ToolCallID]] += len(msg.Text())
				r, ok := callResponse[msg.ToolCallID]
				if t, timed := llm.TimingOf(msg); ok && timed {
					slowest[r] = max(slowest[r], t.Duration.Milliseconds())
				}
			}
		}
		if main && c.ToolCount > 0 && c.Error == "" {
			steps++
		}
		if c.Response == nil {
			continue
		}
		responses++
		todoOnly := len(c.Response.ToolCalls) > 0
		for _, call := range c.Response.ToolCalls {
			toolNames[call.ID] = call.Name
			if !main {
				continue
			}
			callResponse[call.ID] = responses
			todoOnly = todoOnly && call.Name == "write_todos"
			key := call.Name + "\x00" + hashJSON(call.Args)
			seenArgs[key]++
			if seenArgs[key] > 1 {
				p.DuplicateToolCalls++
			}
			p.MaxRepeat = max(p.MaxRepeat, seenArgs[key])
			if p.FirstGoldSrcTouchStep == nil && slices.ContainsFunc(touchedPaths(call), func(f string) bool { return goldSrc[f] }) {
				step := steps
				p.FirstGoldSrcTouchStep = &step
			}
		}
		if main && todoOnly {
			p.TodoOnlyTurns++
		}
	}
	for _, ms := range slowest {
		p.ToolWallMS += ms
	}
	p.VerifyBlocks = len(verifies)
	return p
}

// touchedPaths returns the repository paths a file tool call reads or
// changes, without the virtual filesystem's leading slash.
func touchedPaths(call llm.ToolCall) []string {
	var raw []string
	switch call.Name {
	case "read_file", "write_file", "edit_file":
		if s, _ := call.Args["file_path"].(string); s != "" {
			raw = append(raw, s)
		}
	case "apply_patch":
		text, _ := call.Args["patch"].(string)
		for _, m := range patchFileLine.FindAllStringSubmatch(text, -1) {
			raw = append(raw, m[1])
		}
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		out = append(out, strings.TrimPrefix(path.Clean("/"+s), "/"))
	}
	return out
}

// GoldSourcePaths keeps the gold files that are neither tests nor
// documentation: the files a fix must change.
func GoldSourcePaths(gold []string) []string {
	var out []string
	for _, g := range gold {
		if !IsTestPath(g) && !IsDocPath(g) {
			out = append(out, g)
		}
	}
	return out
}

// IsDocPath reports whether p is documentation or a changelog fragment.
func IsDocPath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".md", ".rst", ".txt", ".adoc":
		return true
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "docs", "doc", "changelogs", "changelog":
			return true
		}
	}
	return false
}

// SourceRecall is the share of the gold source files the predicted patch
// changed; ok is false when the gold patch has no source file.
func SourceRecall(predicted, gold []string) (float64, bool) {
	src := GoldSourcePaths(gold)
	if len(src) == 0 {
		return 0, false
	}
	hit := 0
	for _, g := range src {
		if slices.Contains(predicted, g) {
			hit++
		}
	}
	return float64(hit) / float64(len(src)), true
}

func hashJSON(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}
