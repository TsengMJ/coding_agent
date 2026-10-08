package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The advisory lock serializes concurrent CREATE TABLE IF NOT EXISTS, which
// otherwise races on pg_type and fails.
const postgresStoreSchema = `
SELECT pg_advisory_xact_lock(hashtext('quill_store_schema'));
CREATE TABLE IF NOT EXISTS quill_store (
	prefix     TEXT NOT NULL,
	key        TEXT NOT NULL,
	value      JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (prefix, key)
);
CREATE INDEX IF NOT EXISTS quill_store_prefix ON quill_store (prefix text_pattern_ops);
`

// PostgresStore keeps items in the quill_store table; they never expire.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates the table if needed. The caller owns the pool.
func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("runtime.NewPostgresStore: nil pool")
	}
	if _, err := pool.Exec(ctx, postgresStoreSchema); err != nil {
		return nil, fmt.Errorf("store: create quill_store: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Get(ctx context.Context, ns Namespace, key string) (*Item, error) {
	if err := ValidateNamespace(ns); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `SELECT prefix, key, value, created_at, updated_at FROM quill_store WHERE prefix = $1 AND key = $2`,
		joinNamespace(ns), key)
	it, err := scanStoreItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get %s/%s: %w", joinNamespace(ns), key, err)
	}
	return &it, nil
}

func (s *PostgresStore) Put(ctx context.Context, ns Namespace, key string, value map[string]any) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("store: encode value: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
INSERT INTO quill_store (prefix, key, value) VALUES ($1, $2, $3)
ON CONFLICT (prefix, key) DO UPDATE SET value = EXCLUDED.value, updated_at = clock_timestamp()`,
		joinNamespace(ns), key, raw)
	if err != nil {
		return fmt.Errorf("store: put %s/%s: %w", joinNamespace(ns), key, err)
	}
	return nil
}

func (s *PostgresStore) Delete(ctx context.Context, ns Namespace, key string) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM quill_store WHERE prefix = $1 AND key = $2`, joinNamespace(ns), key); err != nil {
		return fmt.Errorf("store: delete %s/%s: %w", joinNamespace(ns), key, err)
	}
	return nil
}

func (s *PostgresStore) Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error) {
	if err := checkSearch(prefix, opts); err != nil {
		return nil, err
	}
	p := joinNamespace(prefix)
	rows, err := s.pool.Query(ctx, `
SELECT prefix, key, value, created_at, updated_at FROM quill_store
WHERE $1 = '' OR prefix = $1 OR prefix LIKE $2 ESCAPE '\'`, p, escapeLike(p)+namespaceSep+"%")
	if err != nil {
		return nil, fmt.Errorf("store: search %s: %w", p, err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		it, err := scanStoreItem(rows)
		if err != nil {
			return nil, fmt.Errorf("store: search %s: %w", p, err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: search %s: %w", p, err)
	}
	return finishSearch(out, opts), nil
}

func scanStoreItem(row pgx.Row) (Item, error) {
	var it Item
	var prefix string
	var raw []byte
	if err := row.Scan(&prefix, &it.Key, &raw, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return Item{}, err
	}
	if err := json.Unmarshal(raw, &it.Value); err != nil {
		return Item{}, fmt.Errorf("decode value: %w", err)
	}
	it.Namespace = splitNamespace(prefix)
	return it, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}
