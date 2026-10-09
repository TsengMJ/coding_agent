package sessions

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresSchema = `
CREATE TABLE IF NOT EXISTS quill_threads (
	thread_id     TEXT PRIMARY KEY,
	cwd           TEXT NOT NULL,
	title         TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL,
	updated_at    TIMESTAMPTZ NOT NULL,
	message_count INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT '';
ALTER TABLE quill_threads ADD COLUMN IF NOT EXISTS owner TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS quill_threads_cwd_updated ON quill_threads (cwd, updated_at DESC);
`

// PostgresStore keeps the catalog in the quill_threads table.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates the table if needed. The caller owns the pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("sessions.NewPostgresStore: nil pool")
	}
	if _, err := pool.Exec(ctx, postgresSchema); err != nil {
		return nil, fmt.Errorf("sessions: create quill_threads: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Touch(ctx context.Context, info Info) error {
	if info.UpdatedAt.IsZero() {
		info.UpdatedAt = time.Now()
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = info.UpdatedAt
	}
	_, err := s.pool.Exec(ctx, `
INSERT INTO quill_threads (thread_id, cwd, title, created_at, updated_at, message_count, model, workspace_id, owner)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (thread_id) DO UPDATE SET
	title = CASE WHEN quill_threads.title = '' THEN EXCLUDED.title ELSE quill_threads.title END,
	updated_at = EXCLUDED.updated_at,
	message_count = EXCLUDED.message_count,
	model = CASE WHEN EXCLUDED.model = '' THEN quill_threads.model ELSE EXCLUDED.model END`,
		info.ThreadID, info.Cwd, info.Title, info.CreatedAt, info.UpdatedAt, info.MessageCount, info.Model, info.WorkspaceID, info.Owner)
	if err != nil {
		return fmt.Errorf("sessions: touch %s: %w", info.ThreadID, err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, threadID string) (*Info, error) {
	row := s.pool.QueryRow(ctx, `
SELECT thread_id, cwd, title, created_at, updated_at, message_count, model, workspace_id, owner
FROM quill_threads WHERE thread_id = $1`, threadID)
	info, err := scanInfo(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: get %s: %w", threadID, err)
	}
	return &info, nil
}

func (s *PostgresStore) SetTitle(ctx context.Context, threadID, title string) (*Info, error) {
	row := s.pool.QueryRow(ctx, `
UPDATE quill_threads SET title = $2 WHERE thread_id = $1
RETURNING thread_id, cwd, title, created_at, updated_at, message_count, model, workspace_id, owner`, threadID, title)
	info, err := scanInfo(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: rename %s: %w", threadID, err)
	}
	return &info, nil
}

func (s *PostgresStore) List(ctx context.Context, f Filter) ([]Info, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.pool.Query(ctx, `
SELECT thread_id, cwd, title, created_at, updated_at, message_count, model, workspace_id, owner
FROM quill_threads
WHERE ($1 = '' OR cwd = $1) AND ($3 = '' OR workspace_id = $3) AND ($4 = '' OR owner = $4)
ORDER BY updated_at DESC
LIMIT NULLIF($2, -1)`, f.Cwd, limit, f.WorkspaceID, f.Owner)
	if err != nil {
		return nil, fmt.Errorf("sessions: list: %w", err)
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		info, err := scanInfo(rows)
		if err != nil {
			return nil, fmt.Errorf("sessions: list: %w", err)
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Delete(ctx context.Context, threadID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM quill_threads WHERE thread_id = $1`, threadID); err != nil {
		return fmt.Errorf("sessions: delete %s: %w", threadID, err)
	}
	return nil
}

func scanInfo(row pgx.Row) (Info, error) {
	var info Info
	err := row.Scan(&info.ThreadID, &info.Cwd, &info.Title, &info.CreatedAt, &info.UpdatedAt, &info.MessageCount, &info.Model, &info.WorkspaceID, &info.Owner)
	return info, err
}
