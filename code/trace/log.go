package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemoryLog keeps trace lines in process memory (tests).
type MemoryLog struct {
	mu    sync.Mutex
	lines map[string][][]byte
}

// NewMemoryLog returns an empty in-memory log.
func NewMemoryLog() *MemoryLog { return &MemoryLog{lines: map[string][][]byte{}} }

func (m *MemoryLog) Append(_ context.Context, threadID string, lines [][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, l := range lines {
		m.lines[threadID] = append(m.lines[threadID], append([]byte(nil), l...))
	}
	return nil
}

func (m *MemoryLog) Each(_ context.Context, threadID string, fn func([]byte) bool) error {
	m.mu.Lock()
	lines := append([][]byte(nil), m.lines[threadID]...)
	m.mu.Unlock()
	for _, l := range lines {
		if !fn(l) {
			return nil
		}
	}
	return nil
}

// Lines returns a copy of threadID's lines.
func (m *MemoryLog) Lines(threadID string) [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte(nil), m.lines[threadID]...)
}

// FileLog reads a trace.jsonl written by earlier dcode versions, which kept
// one file per thread. It is read-only and ignores the thread ID.
type FileLog struct{ Path string }

func (FileLog) Append(context.Context, string, [][]byte) error {
	return errors.New("trace: FileLog is read-only")
}

func (f FileLog) Each(_ context.Context, _ string, fn func([]byte) bool) error {
	file, err := os.Open(f.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if !fn(append([]byte(nil), sc.Bytes()...)) {
			return nil
		}
	}
	return sc.Err()
}

// Lines are BYTEA rather than JSONB: tool output may hold NUL characters,
// which JSONB rejects. The advisory lock serializes concurrent CREATE TABLE IF
// NOT EXISTS, which otherwise races on pg_type and fails.
const postgresLogSchema = `
SELECT pg_advisory_xact_lock(hashtext('dcode_trace_schema'));
CREATE TABLE IF NOT EXISTS dcode_trace_blobs (
	hash TEXT PRIMARY KEY,
	line BYTEA NOT NULL
);
CREATE TABLE IF NOT EXISTS dcode_trace_records (
	id         BIGSERIAL PRIMARY KEY,
	thread_id  TEXT NOT NULL,
	type       TEXT NOT NULL,
	blob_hash  TEXT REFERENCES dcode_trace_blobs (hash),
	data       BYTEA,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dcode_trace_records_thread ON dcode_trace_records (thread_id, id);
`

// PostgresLog keeps traces in dcode_trace_records. A blob line goes to
// dcode_trace_blobs once per hash, shared by every thread, and the thread
// keeps a row pointing at it.
type PostgresLog struct {
	pool *pgxpool.Pool
}

// NewPostgresLog creates the tables if needed. The caller owns the pool.
func NewPostgresLog(ctx context.Context, pool *pgxpool.Pool) (*PostgresLog, error) {
	if pool == nil {
		return nil, errors.New("trace.NewPostgresLog: nil pool")
	}
	if _, err := pool.Exec(ctx, postgresLogSchema); err != nil {
		return nil, fmt.Errorf("trace: create tables: %w", err)
	}
	return &PostgresLog{pool: pool}, nil
}

// lineHead is the part of a record Append needs to route it.
type lineHead struct {
	Type string `json:"type"`
	Blob *struct {
		Hash string `json:"hash"`
	} `json:"blob"`
}

func (p *PostgresLog) Append(ctx context.Context, threadID string, lines [][]byte) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		for _, l := range lines {
			var head lineHead
			if err := json.Unmarshal(l, &head); err != nil {
				return fmt.Errorf("decode record: %w", err)
			}
			if head.Blob != nil && head.Blob.Hash != "" {
				if _, err := tx.Exec(ctx, `INSERT INTO dcode_trace_blobs (hash, line) VALUES ($1, $2) ON CONFLICT (hash) DO NOTHING`,
					head.Blob.Hash, l); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO dcode_trace_records (thread_id, type, blob_hash) VALUES ($1, $2, $3)`,
					threadID, head.Type, head.Blob.Hash); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO dcode_trace_records (thread_id, type, data) VALUES ($1, $2, $3)`,
				threadID, head.Type, l); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *PostgresLog) Each(ctx context.Context, threadID string, fn func([]byte) bool) error {
	rows, err := p.pool.Query(ctx, `
SELECT COALESCE(b.line, r.data) FROM dcode_trace_records r
LEFT JOIN dcode_trace_blobs b ON b.hash = r.blob_hash
WHERE r.thread_id = $1 ORDER BY r.id`, threadID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line []byte
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if !fn(line) {
			return nil
		}
	}
	return rows.Err()
}

// EachType reads record payloads directly and deliberately avoids joining the
// blob table. Summary callers therefore do not transfer repeated prompts,
// tool schemas or tool-output blobs.
func (p *PostgresLog) EachType(ctx context.Context, threadID string, types []string, fn func([]byte) bool) error {
	rows, err := p.pool.Query(ctx, `
SELECT data FROM dcode_trace_records
WHERE thread_id = $1 AND type = ANY($2)
ORDER BY id`, threadID, types)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var line []byte
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if !fn(line) {
			return nil
		}
	}
	return rows.Err()
}

func (p *PostgresLog) Blobs(ctx context.Context, hashes []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := p.pool.Query(ctx, `SELECT hash, line FROM dcode_trace_blobs WHERE hash = ANY($1)`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var hash string
		var line []byte
		if err := rows.Scan(&hash, &line); err != nil {
			return nil, err
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			return nil, err
		}
		if rec.Blob != nil {
			out[hash] = rec.Blob.Data
		}
	}
	return out, rows.Err()
}

// DeleteThread removes threadID's records; shared blobs stay.
func (p *PostgresLog) DeleteThread(ctx context.Context, threadID string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM dcode_trace_records WHERE thread_id = $1`, threadID)
	return err
}
