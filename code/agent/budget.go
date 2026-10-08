package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	core "github.com/tsengmingchun/quill/agent"
	"github.com/tsengmingchun/quill/llm"
	rt "github.com/tsengmingchun/quill/runtime"
)

const (
	// BudgetSource marks the request-only message that warns about a
	// nearly spent run budget.
	BudgetSource = "budget"
	// budgetWarnAt is the share of the time or step budget after which
	// every request carries the warning.
	budgetWarnAt = 0.8
)

// budgetNotice tells the model to wrap up once 80% of the run's deadline
// or step limit is spent, so a long run ends with a finished change and a
// final answer instead of being cut off mid-investigation. The warning is
// a message after the conversation, so the cached prefix is unchanged.
type budgetNotice struct {
	now    func() time.Time
	mu     sync.Mutex
	starts map[string]time.Time
}

func newBudgetNotice() *budgetNotice {
	return &budgetNotice{now: time.Now, starts: map[string]time.Time{}}
}

func (*budgetNotice) Name() string { return "BudgetNoticeMiddleware" }

func (b *budgetNotice) WrapModelCall(ctx context.Context, req *core.ModelRequest, next core.ModelHandler) (*core.ModelResponse, error) {
	var spent []string
	thread, limit := "", 0
	if req.Runtime != nil {
		thread, limit = req.Runtime.Config.ThreadID, req.Runtime.Config.RecursionLimit
	}
	if deadline, ok := ctx.Deadline(); ok {
		start := b.start(thread)
		total, used := deadline.Sub(start), b.now().Sub(start)
		if total > 0 && float64(used) >= budgetWarnAt*float64(total) {
			spent = append(spent, fmt.Sprintf("%.0f of %.0f minutes", used.Minutes(), total.Minutes()))
		}
	}
	if limit > 0 && req.State != nil {
		if steps := turnSteps(req.State.Messages); float64(steps) >= budgetWarnAt*float64(limit) {
			spent = append(spent, fmt.Sprintf("%d of %d steps", steps, limit))
		}
	}
	if len(spent) == 0 {
		return next(ctx, req)
	}
	tail := requestTail(BudgetSource, fmt.Sprintf("[budget] This run has used %s. Wrap up now: finish the change you are making, "+
		"run the relevant tests once, and give your final answer, saying what is unverified. Do not start new investigations.",
		strings.Join(spent, " and ")))
	updated := *req
	updated.Messages = append(req.Messages[:len(req.Messages):len(req.Messages)], tail)
	return next(ctx, &updated)
}

// start returns when the thread's first model call of this agent was made.
func (b *budgetNotice) start(thread string) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.starts[thread]
	if !ok {
		s = b.now()
		b.starts[thread] = s
	}
	return s
}

// turnSteps counts the model replies since the last user message, which is
// how many steps the current run has taken.
func turnSteps(msgs []llm.Message) int {
	steps := 0
	for i := len(msgs) - 1; i >= 0 && !rt.IsUserTurn(msgs[i]); i-- {
		if msgs[i].Role == llm.RoleAssistant {
			steps++
		}
	}
	return steps
}
