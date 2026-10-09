// Package storage opens everything quill persists. Content that must survive
// a restart (threads, sessions, plans, traces, conversation history) goes to
// Postgres; content that can be recomputed (evicted tool results, fetched
// pages, search and docs caches) goes to Redis with a TTL.
package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/tsengmingchun/quill/apps/agent/code/audit"
	"github.com/tsengmingchun/quill/apps/agent/code/auth"
	"github.com/tsengmingchun/quill/apps/agent/code/cache"
	"github.com/tsengmingchun/quill/apps/agent/code/sessions"
	"github.com/tsengmingchun/quill/apps/agent/code/trace"
	"github.com/tsengmingchun/quill/apps/agent/code/workspace"
	"github.com/tsengmingchun/quill/apps/agent/middleware/planmode"
	"github.com/tsengmingchun/quill/apps/agent/migrate"
	"github.com/tsengmingchun/quill/apps/agent/runtime"
)

// Kinds reported by Storage.Kind.
const (
	KindPostgres = "postgres"
	KindMemory   = "memory"
)

// PostgresConnFromEnv returns $QUILL_POSTGRES_CONN_STRING, else
// $POSTGRES_CONN_STRING.
func PostgresConnFromEnv() string {
	return firstEnv("QUILL_POSTGRES_CONN_STRING", "POSTGRES_CONN_STRING")
}

// RedisURLFromEnv returns $QUILL_REDIS_URL, else $REDIS_URL.
func RedisURLFromEnv() string { return firstEnv("QUILL_REDIS_URL", "REDIS_URL") }

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// redisPrefix starts every Redis key quill writes.
const redisPrefix = "quill"

// Storage holds the stores of one process.
type Storage struct {
	// Kind is KindPostgres, or KindMemory in tests.
	Kind         string
	Checkpointer runtime.Checkpointer
	Sessions     sessions.Store
	Plans        planmode.Store
	Traces       trace.Log
	Workspaces   workspace.Store
	Audit        audit.Log
	// Users holds GitHub logins and session tokens of the remote service.
	Users auth.Store
	// Durable is a key-value store in Postgres.
	Durable runtime.Store
	// Cache holds short-lived lookups (search results, docs).
	Cache cache.Cache
	// Locks keep a thread to one run at a time across processes.
	Locks ThreadLocks

	expiring func(ttl time.Duration) runtime.Store
	ping     map[string]func(context.Context) error
	close    func()
}

// Ping checks each backing service and returns the failures by name
// ("postgres", "redis"); the memory storage has none.
func (s *Storage) Ping(ctx context.Context) map[string]error {
	out := map[string]error{}
	for name, ping := range s.ping {
		out[name] = ping(ctx)
	}
	return out
}

// Expiring returns a key-value store whose items expire ttl after their last
// write (Redis). The memory storage ignores ttl.
func (s *Storage) Expiring(ttl time.Duration) runtime.Store { return s.expiring(ttl) }

// Close releases the connections.
func (s *Storage) Close() {
	if s.close != nil {
		s.close()
	}
}

// Open connects to Postgres and Redis and migrates the schema. Both are
// required: an empty or unreachable one is an error.
func Open(ctx context.Context, postgresConn, redisURL string) (*Storage, error) {
	if postgresConn == "" {
		return nil, errors.New("storage: Postgres is not configured; set QUILL_POSTGRES_CONN_STRING (or POSTGRES_CONN_STRING)")
	}
	if redisURL == "" {
		return nil, errors.New("storage: Redis is not configured; set QUILL_REDIS_URL (or REDIS_URL), e.g. redis://localhost:6379/0")
	}
	ropts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("storage: parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(ropts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("storage: connect to Redis at %s: %w", ropts.Addr, err)
	}
	pg, err := runtime.NewPostgresCheckpointer(ctx, postgresConn)
	if err != nil {
		rdb.Close()
		return nil, fmt.Errorf("storage: connect to Postgres: %w", err)
	}
	closeAll := func() {
		pg.Close()
		_ = rdb.Close()
	}
	fail := func(what string, err error) (*Storage, error) {
		closeAll()
		return nil, fmt.Errorf("storage: %s: %w", what, err)
	}
	pool := pg.Pool()
	if err := migrate.Up(ctx, pool, migrate.Quill()); err != nil {
		return fail("migrate", err)
	}
	sess, err := sessions.NewPostgresStore(ctx, pool)
	if err != nil {
		return fail("sessions", err)
	}
	plans, err := NewPostgresPlans(ctx, pool)
	if err != nil {
		return fail("plans", err)
	}
	traces, err := trace.NewPostgresLog(ctx, pool)
	if err != nil {
		return fail("traces", err)
	}
	durable, err := runtime.NewPostgresStore(ctx, pool)
	if err != nil {
		return fail("store", err)
	}
	workspaces, err := workspace.NewPostgresStore(ctx, pool)
	if err != nil {
		return fail("workspaces", err)
	}
	auditLog, err := audit.NewPostgresLog(ctx, pool)
	if err != nil {
		return fail("audit", err)
	}
	users, err := auth.NewPostgresStore(ctx, pool)
	if err != nil {
		return fail("users", err)
	}
	locks, err := NewPostgresLocks(ctx, postgresConn)
	if err != nil {
		return fail("locks", err)
	}
	return &Storage{
		Kind:         KindPostgres,
		Checkpointer: pg,
		Sessions:     sess,
		Plans:        plans,
		Traces:       traces,
		Workspaces:   workspaces,
		Audit:        auditLog,
		Users:        users,
		Durable:      durable,
		Cache:        cache.NewRedis(rdb, redisPrefix+":cache"),
		Locks:        locks,
		ping: map[string]func(context.Context) error{
			"postgres": pool.Ping,
			"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		},
		expiring: func(ttl time.Duration) runtime.Store {
			return runtime.NewRedisStore(rdb, runtime.RedisStoreOptions{Prefix: redisPrefix, TTL: ttl})
		},
		close: func() {
			locks.Close()
			closeAll()
		},
	}, nil
}

// NewMemory keeps everything in process memory, for tests.
func NewMemory() *Storage {
	expiring := runtime.NewInMemoryStore()
	return &Storage{
		Kind:         KindMemory,
		Checkpointer: runtime.NewMemoryCheckpointer(),
		Sessions:     sessions.NewMemoryStore(),
		Plans:        planmode.NewMemoryStore(),
		Traces:       trace.NewMemoryLog(),
		Workspaces:   workspace.NewMemoryStore(),
		Audit:        audit.NewMemoryLog(),
		Users:        auth.NewMemoryStore(),
		Durable:      runtime.NewInMemoryStore(),
		Cache:        cache.NewMemory(),
		Locks:        NewMemoryLocks(),
		expiring:     func(time.Duration) runtime.Store { return expiring },
	}
}
