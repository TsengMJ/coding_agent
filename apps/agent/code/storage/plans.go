package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
)

// The advisory lock serializes concurrent CREATE TABLE IF NOT EXISTS, which
// otherwise races on pg_type and fails.
const plansSchema = `
SELECT pg_advisory_xact_lock(hashtext('quill_plans_schema'));
CREATE TABLE IF NOT EXISTS quill_plans (
	id         TEXT PRIMARY KEY,
	thread_id  TEXT NOT NULL,
	name       TEXT NOT NULL,
	status     TEXT NOT NULL,
	plan       JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS quill_plans_thread ON quill_plans (thread_id, updated_at DESC);
`

// PostgresPlans keeps plans in quill_plans.
type PostgresPlans struct {
	pool *pgxpool.Pool
}

// NewPostgresPlans creates the table if needed. The caller owns the pool.
func NewPostgresPlans(ctx context.Context, pool *pgxpool.Pool) (*PostgresPlans, error) {
	if _, err := pool.Exec(ctx, plansSchema); err != nil {
		return nil, fmt.Errorf("create quill_plans: %w", err)
	}
	return &PostgresPlans{pool: pool}, nil
}

func (s *PostgresPlans) Save(ctx context.Context, threadID string, p planmode.Plan) error {
	if p.ID == "" {
		return errors.New("plan has no id")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO quill_plans (id, thread_id, name, status, plan) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (id) DO UPDATE SET thread_id = EXCLUDED.thread_id, name = EXCLUDED.name,
	status = EXCLUDED.status, plan = EXCLUDED.plan, updated_at = now()`,
		p.ID, threadID, p.Name, p.Status, raw)
	return err
}

func (s *PostgresPlans) Get(ctx context.Context, id string) (*planmode.Plan, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT plan FROM quill_plans WHERE id = $1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p planmode.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("decode plan %s: %w", id, err)
	}
	return &p, nil
}

// Delete removes a plan; a missing one is not an error.
func (s *PostgresPlans) Delete(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM quill_plans WHERE id = $1`, id)
	return err
}
