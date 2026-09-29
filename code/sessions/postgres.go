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
CREATE TABLE IF NOT EXISTS dcode_threads (
	thread_id     TEXT PRIMARY KEY,
	cwd           TEXT NOT NULL,
	title         TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL,
	updated_at    TIMESTAMPTZ NOT NULL,
	message_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS dcode_threads_cwd_updated ON dcode_threads (cwd, updated_at DESC);
`

// PostgresStore keeps the catalog in the dcode_threads table.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates the table if needed. The caller owns the pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("sessions.NewPostgresStore: nil pool")
	}
	if _, err := pool.Exec(ctx, postgresSchema); err != nil {
		return nil, fmt.Errorf("sessions: create dcode_threads: %w", err)
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
INSERT INTO dcode_threads (thread_id, cwd, title, created_at, updated_at, message_count)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (thread_id) DO UPDATE SET
	title = CASE WHEN dcode_threads.title = '' THEN EXCLUDED.title ELSE dcode_threads.title END,
	updated_at = EXCLUDED.updated_at,
	message_count = EXCLUDED.message_count`,
		info.ThreadID, info.Cwd, info.Title, info.CreatedAt, info.UpdatedAt, info.MessageCount)
	if err != nil {
		return fmt.Errorf("sessions: touch %s: %w", info.ThreadID, err)
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, threadID string) (*Info, error) {
	row := s.pool.QueryRow(ctx, `
SELECT thread_id, cwd, title, created_at, updated_at, message_count
FROM dcode_threads WHERE thread_id = $1`, threadID)
	info, err := scanInfo(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessions: get %s: %w", threadID, err)
	}
	return &info, nil
}

func (s *PostgresStore) List(ctx context.Context, f Filter) ([]Info, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.pool.Query(ctx, `
SELECT thread_id, cwd, title, created_at, updated_at, message_count
FROM dcode_threads
WHERE $1 = '' OR cwd = $1
ORDER BY updated_at DESC
LIMIT NULLIF($2, -1)`, f.Cwd, limit)
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
	if _, err := s.pool.Exec(ctx, `DELETE FROM dcode_threads WHERE thread_id = $1`, threadID); err != nil {
		return fmt.Errorf("sessions: delete %s: %w", threadID, err)
	}
	return nil
}

func scanInfo(row pgx.Row) (Info, error) {
	var info Info
	err := row.Scan(&info.ThreadID, &info.Cwd, &info.Title, &info.CreatedAt, &info.UpdatedAt, &info.MessageCount)
	return info, err
}
