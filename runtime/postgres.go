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
const postgresCheckpointTable = "deepagents_checkpoints"

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
