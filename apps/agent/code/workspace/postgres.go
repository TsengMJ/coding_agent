package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore keeps workspaces in the quill_workspaces table.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore uses quill_workspaces, created by the migrate package. The caller
// owns the pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("workspace.NewPostgresStore: nil pool")
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Put(ctx context.Context, ws Workspace) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO quill_workspaces (id, owner, repo_url, base_branch, image, dir, state, created_at, last_used_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
	image = EXCLUDED.image, state = EXCLUDED.state, last_used_at = EXCLUDED.last_used_at`,
		ws.ID, ws.Owner, ws.RepoURL, ws.BaseBranch, ws.Image, ws.Dir, string(ws.State), ws.CreatedAt, ws.LastUsedAt)
	if err != nil {
		return fmt.Errorf("workspace: put %s: %w", ws.ID, err)
	}
	return nil
}

const selectColumns = `SELECT id, owner, repo_url, base_branch, image, dir, state, created_at, last_used_at FROM quill_workspaces`

func (s *PostgresStore) Get(ctx context.Context, id string) (*Workspace, error) {
	ws, err := scan(s.pool.QueryRow(ctx, selectColumns+` WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: get %s: %w", id, err)
	}
	return &ws, nil
}

func (s *PostgresStore) List(ctx context.Context, owner string) ([]Workspace, error) {
	rows, err := s.pool.Query(ctx, selectColumns+` WHERE $1 = '' OR owner = $1 ORDER BY created_at DESC`, owner)
	if err != nil {
		return nil, fmt.Errorf("workspace: list: %w", err)
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		ws, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("workspace: list: %w", err)
		}
		out = append(out, ws)
	}
	return out, rows.Err()
}

func (s *PostgresStore) Delete(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM quill_workspaces WHERE id = $1`, id); err != nil {
		return fmt.Errorf("workspace: delete %s: %w", id, err)
	}
	return nil
}

func scan(row pgx.Row) (Workspace, error) {
	var ws Workspace
	var state string
	err := row.Scan(&ws.ID, &ws.Owner, &ws.RepoURL, &ws.BaseBranch, &ws.Image, &ws.Dir, &state, &ws.CreatedAt, &ws.LastUsedAt)
	ws.State = State(state)
	return ws, err
}
