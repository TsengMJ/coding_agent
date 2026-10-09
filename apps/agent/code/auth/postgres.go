package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore keeps users and sessions in quill_users and quill_sessions.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore uses quill_users and quill_sessions, created by the migrate package. The caller
// owns the pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("auth.NewPostgresStore: nil pool")
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) PutUser(ctx context.Context, r Record) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO quill_users (id, name, github_token) VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, github_token = EXCLUDED.github_token, last_login_at = now()`,
		r.ID, r.Name, r.EncryptedToken)
	if err != nil {
		return fmt.Errorf("auth: put user %s: %w", r.ID, err)
	}
	return nil
}

func (s *PostgresStore) CreateSession(ctx context.Context, hash, userID string, expires time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM quill_sessions WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("auth: prune sessions: %w", err)
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO quill_sessions (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`, hash, userID, expires)
	if err != nil {
		return fmt.Errorf("auth: create session: %w", err)
	}
	return nil
}

func (s *PostgresStore) LookupSession(ctx context.Context, hash string, now time.Time) (*Record, error) {
	var r Record
	err := s.pool.QueryRow(ctx, `
SELECT u.id, u.name, u.github_token FROM quill_sessions s JOIN quill_users u ON u.id = s.user_id
WHERE s.token_hash = $1 AND s.expires_at > $2`, hash, now).Scan(&r.ID, &r.Name, &r.EncryptedToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSession
	}
	if err != nil {
		return nil, fmt.Errorf("auth: lookup session: %w", err)
	}
	return &r, nil
}

func (s *PostgresStore) DeleteSession(ctx context.Context, hash string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM quill_sessions WHERE token_hash = $1`, hash); err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	return nil
}
