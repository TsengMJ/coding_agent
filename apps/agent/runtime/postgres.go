package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/smallnest/langgraphgo/checkpoint/postgres"
	"github.com/smallnest/langgraphgo/graph"
)

// postgresCheckpointTable is the langgraphgo checkpoint table for this process.
const postgresCheckpointTable = "quill_checkpoints"

// PostgresCheckpointer stores thread checkpoints in PostgreSQL
// (langgraphgo `checkpoint/postgres`). connString is a postgres URL,
// typically the value of POSTGRES_CONN_STRING.
type PostgresCheckpointer struct {
	storeCheckpointer
	pool *pgxpool.Pool
}

// NewPostgresCheckpointer opens a pool, creates the checkpoint table, and
// returns a checkpointer that survives process restarts.
func NewPostgresCheckpointer(ctx context.Context, connString string) (*PostgresCheckpointer, error) {
	if connString == "" {
		return nil, fmt.Errorf("postgres checkpointer: empty connection string")
	}
	pool, err := pgxpool.New(ctx, connString)
	if err != nil {
		return nil, fmt.Errorf("postgres checkpointer: %w", err)
	}
	store := postgres.NewPostgresCheckpointStoreWithPool(pool, postgresCheckpointTable)
	if err := store.InitSchema(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres checkpointer: init schema: %w", err)
	}
	index := pgIndex{pool: pool, table: postgresCheckpointTable}
	if err := index.initSchema(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres checkpointer: init index: %w", err)
	}
	return &PostgresCheckpointer{
		storeCheckpointer: newStoreCheckpointer(store, index),
		pool:              pool,
	}, nil
}

// Pool exposes the connection pool so other tables can share it.
func (c *PostgresCheckpointer) Pool() *pgxpool.Pool { return c.pool }

// Close releases the connection pool.
func (c *PostgresCheckpointer) Close() {
	if c != nil && c.pool != nil {
		c.pool.Close()
	}
}

// pgIndex queries the langgraphgo checkpoint table directly. version holds
// the checkpoint seq; rows written before that have version 0 and their seq
// only inside the state JSON.
type pgIndex struct {
	pool  *pgxpool.Pool
	table string
}

func (p pgIndex) initSchema(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, fmt.Sprintf(
		`CREATE INDEX IF NOT EXISTS idx_%[1]s_thread_version ON %[1]s (execution_id, version DESC, timestamp DESC)`,
		p.table))
	return err
}

func (p pgIndex) latest(ctx context.Context, threadID string) (*graph.Checkpoint, error) {
	var id string
	var state []byte
	err := p.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT id, state FROM %s WHERE execution_id = $1 ORDER BY version DESC, timestamp DESC LIMIT 1`,
		p.table), threadID).Scan(&id, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(state, &cp); err != nil {
		return nil, fmt.Errorf("checkpoint %s: %w", id, err)
	}
	return &graph.Checkpoint{ID: id, State: cp}, nil
}

func (p pgIndex) maxSeq(ctx context.Context, threadID string) (int64, error) {
	var seq int64
	err := p.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT GREATEST(
			COALESCE(MAX(version), 0),
			COALESCE(MAX((state->>'seq')::bigint) FILTER (WHERE version = 0), 0))
		FROM %s WHERE execution_id = $1`, p.table), threadID).Scan(&seq)
	return seq, err
}

func (p pgIndex) prune(ctx context.Context, threadID string, keep int) error {
	_, err := p.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %[1]s WHERE execution_id = $1 AND id NOT IN (
			SELECT id FROM %[1]s WHERE execution_id = $1
			ORDER BY version DESC, timestamp DESC LIMIT $2)`, p.table), threadID, keep)
	return err
}

// HistoryPage reads only a window of messages from PostgreSQL. The checkpoint
// remains the source of truth for agent resume; this is solely a read model for
// the GUI, so opening a long thread does not transfer its whole project state.
func (c *PostgresCheckpointer) HistoryPage(ctx context.Context, threadID string, before, limit int) (*HistoryPage, error) {
	if limit <= 0 {
		limit = 20
	}
	var messages, values, interrupts []byte
	var seq int64
	var start, total int
	err := c.pool.QueryRow(ctx, fmt.Sprintf(`
WITH latest AS (
  SELECT state FROM %s WHERE execution_id = $1
  ORDER BY version DESC, timestamp DESC LIMIT 1
), numbered AS (
  SELECT value AS message, ordinality::int AS pos
  FROM latest, jsonb_array_elements(COALESCE(latest.state->'state'->'messages', '[]'::jsonb)) WITH ORDINALITY
), bounds AS (
  SELECT COALESCE(NULLIF($2, 0), (SELECT COALESCE(MAX(pos), 0) + 1 FROM numbered)) AS upper
), page_start AS (
  SELECT COALESCE((SELECT pos FROM numbered, bounds
    WHERE pos < bounds.upper AND message->>'role' = 'user' AND message->'metadata'->'lc_source' IS NULL
    ORDER BY pos DESC OFFSET GREATEST($3 - 1, 0) LIMIT 1), 1) AS pos
)
SELECT
  COALESCE((SELECT jsonb_agg(numbered.message ORDER BY numbered.pos) FROM numbered, bounds, page_start
    WHERE numbered.pos >= page_start.pos AND numbered.pos < bounds.upper), '[]'::jsonb),
  COALESCE((SELECT jsonb_strip_nulls(jsonb_build_object(
    'plan', state->'state'->'values'->'plan',
    'todos', state->'state'->'values'->'todos')) FROM latest), '{}'::jsonb),
  COALESCE((SELECT state->'pending_interrupts' FROM latest), '[]'::jsonb),
  COALESCE((SELECT (state->>'seq')::bigint FROM latest), 0),
  (SELECT pos FROM page_start),
  (SELECT COALESCE(MAX(pos), 0) FROM numbered)`, postgresCheckpointTable), threadID, before, limit).Scan(&messages, &values, &interrupts, &seq, &start, &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("history page %s: %w", threadID, err)
	}
	var out HistoryPage
	out.ThreadID, out.CheckpointSeq = threadID, seq
	if err := json.Unmarshal(messages, &out.Messages); err != nil {
		return nil, fmt.Errorf("history messages: %w", err)
	}
	if err := json.Unmarshal(interrupts, &out.PendingInterrupts); err != nil {
		return nil, fmt.Errorf("history interrupts: %w", err)
	}
	var allValues map[string]any
	if err := json.Unmarshal(values, &allValues); err != nil {
		return nil, fmt.Errorf("history values: %w", err)
	}
	out.Values = map[string]any{}
	for _, key := range []string{"plan", "todos"} {
		if v, ok := allValues[key]; ok {
			out.Values[key] = v
		}
	}
	out.HasMore = start > 1 && total > 0
	out.NextBefore = start
	return &out, nil
}
