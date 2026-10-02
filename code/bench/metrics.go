package bench

import (
	"regexp"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/code/trace"
	"github.com/tsengmingchun/deepagents_golang/llm"
	"github.com/tsengmingchun/deepagents_golang/middleware/subagents"
)

// testCommand recognizes shell commands that run a test suite in the V2
// repositories (Go, Python, JS/TS).
var testCommand = regexp.MustCompile(`\b(go test|pytest|py\.test|python[0-9.]* -m (pytest|unittest)|tox|nox|jest|mocha|vitest|ava|tap|(npm|yarn|pnpm|npx)( run)? test)\b`)

// MetricsFromTrace derives how the agent worked from its recorded model
// calls. Steps count main-agent calls that offered tools, which leaves out
// summarization calls; peak context is the largest main-agent prompt.
func MetricsFromTrace(calls []trace.LLMCallDetail, prices cost.Table) Metrics {
	m := Metrics{ToolCalls: map[string]int{}}
	toolNames := map[string]string{}
	toolSeen := map[string]bool{}
	summaries := map[string]bool{}
	filesRead := map[string]bool{}
	var costSum float64
	priced := true
	for _, c := range calls {
		m.LLMCalls++
		m.ModelMS += c.DurationMs
		main := len(c.Namespace) == 0
		if len(c.Namespace) > 0 && strings.HasPrefix(c.Namespace[0], subagents.NamespacePrefix) {
			m.SubagentLLMCalls++
		}
		if u := c.Usage; u != nil {
			m.TokensIn += u.InputTokens
			m.TokensOut += u.OutputTokens
			m.CacheRead += u.CacheRead
			m.CacheWrite += u.CacheWrite
			if main {
				m.PeakContextTokens = max(m.PeakContextTokens, u.InputTokens)
			}
			usd, err := prices.Estimate(c.Model, *u)
			if err != nil {
				priced = false
			}
			costSum += usd
		}
		for _, msg := range c.Messages {
			switch {
			case main && msg.Role == llm.RoleUser && msg.Metadata["lc_source"] == "summarization":
				summaries[msg.Text()] = true
			case msg.Role == llm.RoleTool && msg.ToolCallID != "" && !toolSeen[msg.ToolCallID]:
				toolSeen[msg.ToolCallID] = true
				if t, ok := llm.TimingOf(msg); ok {
					ms := t.Duration.Milliseconds()
					m.ToolMS += ms
					if toolNames[msg.ToolCallID] == "execute" {
						m.ExecuteMS += ms
					}
				}
			}
		}
		if main && c.ToolCount > 0 && c.Error == "" {
			m.Steps++
		}
		if c.Response == nil {
			continue
		}
		for _, call := range c.Response.ToolCalls {
			toolNames[call.ID] = call.Name
			m.ToolCalls[call.Name]++
			switch call.Name {
			case "execute":
				m.ExecuteCalls++
				if cmd, _ := call.Args["command"].(string); testCommand.MatchString(cmd) {
					m.TestRuns++
				}
			case "read_file":
				if p, _ := call.Args["file_path"].(string); p != "" {
					filesRead[p] = true
				}
			case "write_file", "edit_file", "apply_patch":
				if main && m.FirstEditStep == nil {
					step := m.Steps
					m.FirstEditStep = &step
				}
			}
		}
	}
	m.FilesRead = len(filesRead)
	m.Summarizations = len(summaries)
	if priced && m.LLMCalls > 0 {
		m.CostUSD = &costSum
	}
	return m
}
