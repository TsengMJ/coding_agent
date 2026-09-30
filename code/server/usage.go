package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/tsengmingchun/deepagents_golang/code/cost"
	"github.com/tsengmingchun/deepagents_golang/llm"
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

// ThreadUsage reports a thread's context size and token spend. It counts
// the main agent's replies only; subagent calls are not in the thread state.
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
	ConversationTokens int          `json:"conversation_tokens"`
	FixedTokens        int          `json:"fixed_tokens"`
	Models             []ModelUsage `json:"models"`
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

// computeUsage totals assistant replies by their model_name tag; untagged
// replies count toward fallbackModel.
func computeUsage(msgs []llm.Message, fallbackModel string, prices cost.Table) ThreadUsage {
	var u ThreadUsage
	byModel := map[string]*ModelUsage{}
	last := -1
	for i, m := range msgs {
		if m.Role != llm.RoleAssistant || m.Usage == nil {
			continue
		}
		name, _ := m.Metadata[ModelNameKey].(string)
		if name == "" {
			name = fallbackModel
		}
		mu := byModel[name]
		if mu == nil {
			mu = &ModelUsage{Model: name}
			byModel[name] = mu
		}
		mu.Calls++
		mu.InputTokens += m.Usage.InputTokens
		mu.OutputTokens += m.Usage.OutputTokens
		mu.CacheReadTokens += m.Usage.CacheRead
		mu.CacheWriteTokens += m.Usage.CacheWrite
		last = i
	}
	if last >= 0 {
		u.ContextTokens = msgs[last].Usage.InputTokens
		u.ConversationTokens = llm.ApproximateTokenCounter(msgs[:last], nil)
		u.FixedTokens = max(u.ContextTokens-u.ConversationTokens, 0)
	} else {
		u.ConversationTokens = llm.ApproximateTokenCounter(msgs, nil)
	}
	u.Models = []ModelUsage{}
	for _, mu := range byModel {
		c, err := prices.Estimate(mu.Model, llm.Usage{
			InputTokens: mu.InputTokens, OutputTokens: mu.OutputTokens,
			CacheRead: mu.CacheReadTokens, CacheWrite: mu.CacheWriteTokens,
		})
		if err == nil {
			mu.CostUSD = &c
			u.TotalCostUSD += c
		} else {
			u.Unpriced = append(u.Unpriced, mu.Model)
		}
		u.Models = append(u.Models, *mu)
	}
	slices.SortFunc(u.Models, func(a, b ModelUsage) int { return strings.Compare(a.Model, b.Model) })
	slices.Sort(u.Unpriced)
	return u
}
