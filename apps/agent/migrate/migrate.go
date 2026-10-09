// Package migrate versions quill's Postgres schema. Migrations are numbered
// SQL files embedded in the binary; Up applies the ones a database has not
// seen, each in its own transaction, and records them in
// quill_schema_migrations.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migration is one schema change.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

//go:embed sql/*.sql
var files embed.FS

// Quill returns quill's migrations in version order.
func Quill() []Migration {
	migs, err := load(files, "sql")
	if err != nil {
		panic(err)
	}
	return migs
}

// load reads files named NNNN_name.sql.
func load(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		num, name, ok := strings.Cut(strings.TrimSuffix(e.Name(), ".sql"), "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || v <= 0 {
			return nil, fmt.Errorf("migrate: %s: want NNNN_name.sql", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrate: %s and %s share version %d", prev, e.Name(), v)
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// lockKey serializes Up across processes sharing the database.
const lockKey = "quill_schema_migrations"

// Up applies the migrations the database has not recorded. It refuses a
// database migrated by a newer quill, whose schema this binary may not
// understand.
func Up(ctx context.Context, pool *pgxpool.Pool, migs []Migration) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, lockKey); err != nil {
		return fmt.Errorf("migrate: lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext($1))`, lockKey)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS quill_schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrate: create quill_schema_migrations: %w", err)
	}
	var current int
	if err := conn.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM quill_schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("migrate: read version: %w", err)
	}
	if latest := latestVersion(migs); current > latest {
		return fmt.Errorf("migrate: database schema is at version %d but this quill only knows up to %d; upgrade quill", current, latest)
	}
	for _, m := range migs {
		if m.Version <= current {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: %04d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO quill_schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migrate: record %04d_%s: %w", m.Version, m.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("migrate: commit %04d_%s: %w", m.Version, m.Name, err)
		}
	}
	return nil
}

// Version returns the newest migration the database has applied (0 when
// none).
func Version(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var v int
	err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM quill_schema_migrations`).Scan(&v)
	return v, err
}

func latestVersion(migs []Migration) int {
	v := 0
	for _, m := range migs {
		v = max(v, m.Version)
	}
	return v
}
