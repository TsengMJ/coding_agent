package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrLocked is returned by TryLock when another holder has the thread.
var ErrLocked = errors.New("storage: thread is locked")

// ThreadLocks give one run at a time a thread, across every process that
// shares the storage.
type ThreadLocks interface {
	// TryLock takes the thread's lock without waiting; unlock releases it.
	TryLock(ctx context.Context, threadID string) (unlock func(), err error)
}

// lockConns caps the connections held by running threads; each lock keeps
// one connection for as long as its run lasts.
const lockConns = 64

// PostgresLocks are session advisory locks, each on its own connection, so
// a crashed process releases its locks when Postgres drops the connection.
type PostgresLocks struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	held map[*pgxpool.Conn]bool
}

// NewPostgresLocks opens a pool dedicated to locks, so long runs never
// starve the queries of the main pool.
func NewPostgresLocks(ctx context.Context, connString string) (*PostgresLocks, error) {
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("locks: %w", err)
	}
	cfg.MaxConns = lockConns
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("locks: %w", err)
	}
	return &PostgresLocks{pool: pool, held: map[*pgxpool.Conn]bool{}}, nil
}

// lockSpace keeps thread locks apart from other advisory locks: the
// two-key form never collides with single-key locks such as migrate's.
const lockSpace = "quill_thread"

func (l *PostgresLocks) TryLock(ctx context.Context, threadID string) (func(), error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("locks: %w", err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext($1), hashtext($2))`, lockSpace, threadID).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("locks: lock %s: %w", threadID, err)
	}
	if !ok {
		conn.Release()
		return nil, fmt.Errorf("%w: %s", ErrLocked, threadID)
	}
	l.mu.Lock()
	l.held[conn] = true
	l.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if !l.held[conn] {
				return // Close already dropped it
			}
			delete(l.held, conn)
			_, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext($1), hashtext($2))`, lockSpace, threadID)
			if err != nil {
				// Closing the session is the only other way to drop the lock.
				_ = conn.Hijack().Close(context.Background())
				return
			}
			conn.Release()
		})
	}, nil
}

// Close drops every lock still held and releases the pool. pgxpool.Close
// waits for acquired connections, so held ones are closed first.
func (l *PostgresLocks) Close() {
	l.mu.Lock()
	for conn := range l.held {
		_ = conn.Hijack().Close(context.Background())
	}
	clear(l.held)
	l.mu.Unlock()
	l.pool.Close()
}

// MemoryLocks lock threads within one process.
type MemoryLocks struct {
	mu   sync.Mutex
	held map[string]bool
}

func NewMemoryLocks() *MemoryLocks { return &MemoryLocks{held: map[string]bool{}} }

func (l *MemoryLocks) TryLock(_ context.Context, threadID string) (func(), error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[threadID] {
		return nil, fmt.Errorf("%w: %s", ErrLocked, threadID)
	}
	l.held[threadID] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			delete(l.held, threadID)
		})
	}, nil
}
