package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tsengmingchun/quill/code/cost"
	"github.com/tsengmingchun/quill/llm"
	"github.com/tsengmingchun/quill/middleware/subagents"
)

// PricesFile, in the user config directory, overrides the built-in prices.
const PricesFile = "prices.json"

// ModelUsage totals the replies one model produced in a thread.
type ModelUsage struct {
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	// CostUSD is nil when the model has no price.
	CostUSD *float64 `json:"cost_usd,omitempty"`
}

// ThreadUsage reports a thread's context size and token spend, subagents
// included.
type ThreadUsage struct {
	ThreadID string `json:"thread_id"`
	// Model is the model the thread runs with.
	Model string `json:"model"`
	// ContextTokens is the input size of the latest model call.
	ContextTokens int `json:"context_tokens"`
	// ContextLimit is Model's context window; 0 when unknown.
	ContextLimit   int     `json:"context_limit,omitempty"`
	ContextPercent float64 `json:"context_percent,omitempty"`
	// ConversationTokens estimates the messages sent in the latest call
	// (all messages when there was none); FixedTokens is the rest of
	// ContextTokens: system prompt and tool definitions.
	ConversationTokens int `json:"conversation_tokens"`
	FixedTokens        int `json:"fixed_tokens"`
	// Models totals every reply, the subagents' included; Subagents is the
	// subagents' share.
	Models    []ModelUsage `json:"models"`
	Subagents []ModelUsage `json:"subagents,omitempty"`
	// TotalCostUSD sums the priced models; Unpriced lists the others.
	TotalCostUSD float64  `json:"total_cost_usd"`
	Unpriced     []string `json:"unpriced,omitempty"`
}

// GetUsage returns nil when the thread has no state.
func (s *Service) GetUsage(ctx context.Context, threadID string) (*ThreadUsage, error) {
	if threadID == "" {
		return nil, fmt.Errorf("%w: thread_id is required", ErrBadRequest)
	}
	cp, err := s.checkpointer.Latest(ctx, threadID)
	if err != nil || cp == nil {
		return nil, err
	}
	info, err := s.sessions.Get(ctx, threadID)
	if err != nil {
		return nil, err
	}
	rm, err := s.runModelFor(RunRequest{}, info)
	if err != nil {
		return nil, err
	}
	u := computeUsage(cp.State.Messages, rm.spec, s.prices)
	u.ThreadID = threadID
	u.Model = rm.spec
	if limit := rm.model.Profile().MaxInputTokens; limit > 0 {
		u.ContextLimit = limit
		u.ContextPercent = float64(u.ContextTokens) * 100 / float64(limit)
	}
	return &u, nil
}

// computeUsage totals assistant replies by their model_name tag, and the
// subagent usage recorded on task reports; untagged replies count toward
// fallbackModel.
func computeUsage(msgs []llm.Message, fallbackModel string, prices cost.Table) ThreadUsage {
	var u ThreadUsage
	all, subs := usageTotals{}, usageTotals{}
	last := -1
	for i, m := range msgs {
		for _, s := range subagents.ParseUsage(m.Metadata[subagents.UsageKey]) {
			mu := ModelUsage{Model: s.Model, Calls: s.Calls, InputTokens: s.InputTokens, OutputTokens: s.OutputTokens,
				CacheReadTokens: s.CacheRead, CacheWriteTokens: s.CacheWrite}
			if mu.Model == "" {
				mu.Model = fallbackModel
			}
			all.add(mu)
			subs.add(mu)
		}
		if m.Role != llm.RoleAssistant || m.Usage == nil {
			continue
		}
		name, _ := m.Metadata[ModelNameKey].(string)
		if name == "" {
			name = fallbackModel
		}
		all.add(ModelUsage{Model: name, Calls: 1, InputTokens: m.Usage.InputTokens, OutputTokens: m.Usage.OutputTokens,
			CacheReadTokens: m.Usage.CacheRead, CacheWriteTokens: m.Usage.CacheWrite})
		last = i
	}
	if last >= 0 {
		u.ContextTokens = msgs[last].Usage.InputTokens
		u.ConversationTokens = llm.ApproximateTokenCounter(msgs[:last], nil)
		u.FixedTokens = max(u.ContextTokens-u.ConversationTokens, 0)
	} else {
		u.ConversationTokens = llm.ApproximateTokenCounter(msgs, nil)
	}
	u.Models = all.priced(prices)
	for _, mu := range u.Models {
		if mu.CostUSD != nil {
			u.TotalCostUSD += *mu.CostUSD
		} else {
			u.Unpriced = append(u.Unpriced, mu.Model)
		}
	}
	if len(subs) > 0 {
		u.Subagents = subs.priced(prices)
	}
	return u
}

// usageTotals sums ModelUsage by model.
type usageTotals map[string]*ModelUsage

func (t usageTotals) add(mu ModelUsage) {
	cur := t[mu.Model]
	if cur == nil {
		cur = &ModelUsage{Model: mu.Model}
		t[mu.Model] = cur
	}
	cur.Calls += mu.Calls
	cur.InputTokens += mu.InputTokens
	cur.OutputTokens += mu.OutputTokens
	cur.CacheReadTokens += mu.CacheReadTokens
	cur.CacheWriteTokens += mu.CacheWriteTokens
}

// priced lists the totals by model name, each with its cost when priced.
func (t usageTotals) priced(prices cost.Table) []ModelUsage {
	out := []ModelUsage{}
	for _, mu := range t {
		if c, err := prices.Estimate(mu.Model, llm.Usage{
			InputTokens: mu.InputTokens, OutputTokens: mu.OutputTokens,
			CacheRead: mu.CacheReadTokens, CacheWrite: mu.CacheWriteTokens,
		}); err == nil {
			mu.CostUSD = &c
		}
		out = append(out, *mu)
	}
	slices.SortFunc(out, func(a, b ModelUsage) int { return strings.Compare(a.Model, b.Model) })
	return out
}
