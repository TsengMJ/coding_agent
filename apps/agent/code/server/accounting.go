package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/llm"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// record writes an audit event for the request's user; without login there
// is no one to attribute it to and nothing is written.
func (s *Service) record(ctx context.Context, ev audit.Event) {
	u, ok := auth.UserFrom(ctx)
	if !ok || s.audit == nil {
		return
	}
	ev.User = u.ID
	if err := s.audit.Record(context.WithoutCancel(ctx), ev); err != nil {
		slog.Warn("audit record", "action", ev.Action, "user", ev.User, "err", err)
	}
}

// checkBudget refuses a run once the user has spent the daily limit.
func (s *Service) checkBudget(ctx context.Context) error {
	u, ok := auth.UserFrom(ctx)
	if !ok || s.audit == nil || s.dailyLimit <= 0 {
		return nil
	}
	spent, err := s.audit.SpentSince(ctx, u.ID, audit.StartOfDay(time.Now()))
	if err != nil {
		return err
	}
	if spent >= s.dailyLimit {
		return fmt.Errorf("%w: spent $%.2f of $%.2f today (resets at 00:00 UTC)", ErrQuotaExceeded, spent, s.dailyLimit)
	}
	return nil
}

// recordDecisions logs the approval decisions a resume sends.
func (s *Service) recordDecisions(ctx context.Context, req RunRequest, prev *runtime.Checkpoint) {
	if req.Resume == nil || prev == nil {
		return
	}
	var tools []string
	for _, in := range prev.PendingInterrupts {
		for _, ar := range toInterrupt(in).ActionRequests {
			tools = append(tools, ar.Name)
		}
	}
	types := make([]string, 0, len(req.Resume.Decisions))
	for _, d := range req.Resume.Decisions {
		types = append(types, string(d.Type))
	}
	s.record(ctx, audit.Event{Action: audit.ActionDecision, ThreadID: req.ThreadID, WorkspaceID: req.WorkspaceID,
		Detail: map[string]any{"tools": tools, "decisions": types}})
}

// recordRun logs a finished run with the model spend it added to the
// thread, and returns that spend.
func (s *Service) recordRun(ctx context.Context, req RunRequest, model string, before, after []llm.Message, runErr error) float64 {
	spent := computeUsage(after, model, s.prices).TotalCostUSD - computeUsage(before, model, s.prices).TotalCostUSD
	spent = max(spent, 0)
	detail := map[string]any{"model": model}
	if runErr != nil {
		detail["error"] = runErr.Error()
	}
	s.record(ctx, audit.Event{Action: audit.ActionRun, ThreadID: req.ThreadID, WorkspaceID: req.WorkspaceID,
		CostUSD: spent, Detail: detail})
	return spent
}

// AuditLog returns the request user's recent audit events.
func (s *Service) AuditLog(ctx context.Context, limit int) ([]audit.Event, error) {
	u, ok := auth.UserFrom(ctx)
	if !ok || s.audit == nil {
		return []audit.Event{}, nil
	}
	return s.audit.List(ctx, audit.Filter{User: u.ID, Limit: limit})
}
