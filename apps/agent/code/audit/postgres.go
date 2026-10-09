package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresLog keeps events in the quill_audit table.
type PostgresLog struct {
	pool *pgxpool.Pool
}

// NewPostgresLog uses quill_audit, created by the migrate package. The caller
// owns the pool.
func NewPostgresLog(ctx context.Context, pool *pgxpool.Pool) (*PostgresLog, error) {
	if pool == nil {
		return nil, errors.New("audit.NewPostgresLog: nil pool")
	}
	return &PostgresLog{pool: pool}, nil
}

func (l *PostgresLog) Record(ctx context.Context, ev Event) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	var detail []byte
	if len(ev.Detail) > 0 {
		var err error
		if detail, err = json.Marshal(ev.Detail); err != nil {
			return fmt.Errorf("audit: encode detail: %w", err)
		}
	}
	_, err := l.pool.Exec(ctx, `
INSERT INTO quill_audit (at, user_id, action, thread_id, workspace_id, cost_usd, detail)
VALUES ($1, $2, $3, $4, $5, $6, $7)`, ev.Time, ev.User, ev.Action, ev.ThreadID, ev.WorkspaceID, ev.CostUSD, detail)
	if err != nil {
		return fmt.Errorf("audit: record %s: %w", ev.Action, err)
	}
	return nil
}

func (l *PostgresLog) List(ctx context.Context, f Filter) ([]Event, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := l.pool.Query(ctx, `
SELECT at, user_id, action, thread_id, workspace_id, cost_usd, detail FROM quill_audit
WHERE ($1 = '' OR user_id = $1) AND at >= $2
ORDER BY at DESC, id DESC LIMIT $3`, f.User, f.Since, limit)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		var detail []byte
		if err := rows.Scan(&ev.Time, &ev.User, &ev.Action, &ev.ThreadID, &ev.WorkspaceID, &ev.CostUSD, &detail); err != nil {
			return nil, fmt.Errorf("audit: list: %w", err)
		}
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &ev.Detail)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (l *PostgresLog) SpentSince(ctx context.Context, user string, t time.Time) (float64, error) {
	var sum float64
	err := l.pool.QueryRow(ctx, `
SELECT COALESCE(SUM(cost_usd), 0) FROM quill_audit WHERE user_id = $1 AND action = $2 AND at >= $3`,
		user, ActionRun, t).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("audit: spent: %w", err)
	}
	return sum, nil
}
