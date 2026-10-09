package migrate

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLoadOrdersAndValidatesNames(t *testing.T) {
	migs, err := load(fstest.MapFS{
		"sql/0002_two.sql": {Data: []byte("SELECT 2")},
		"sql/0001_one.sql": {Data: []byte("SELECT 1")},
	}, "sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 2 || migs[0].Version != 1 || migs[1].Name != "two" {
		t.Fatalf("migrations = %+v", migs)
	}
	for _, bad := range []string{"sql/one.sql", "sql/0000_zero.sql"} {
		if _, err := load(fstest.MapFS{bad: {Data: []byte("x")}}, "sql"); err == nil {
			t.Fatalf("%s was accepted", bad)
		}
	}
	if _, err := load(fstest.MapFS{"sql/0001_a.sql": {}, "sql/0001_b.sql": {}}, "sql"); err == nil {
		t.Fatal("duplicate versions were accepted")
	}
}

func TestQuillMigrationsLoad(t *testing.T) {
	if migs := Quill(); len(migs) == 0 || migs[0].Version != 1 {
		t.Fatalf("Quill() = %+v", migs)
	}
}

// schemaPool connects to a fresh Postgres schema dropped after the test.
func schemaPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("quill_migrate_test_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(conn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	return pool
}

func columns(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 ORDER BY column_name`, table)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	return strings.Join(cols, ",")
}

func TestUpMigratesAnEmptyDatabase(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	if err := Up(ctx, pool, Quill()); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool, Quill()); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	v, err := Version(ctx, pool)
	if err != nil || v != latestVersion(Quill()) {
		t.Fatalf("version = %d, %v", v, err)
	}
	for _, table := range []string{"quill_checkpoints", "quill_threads", "quill_workspaces", "quill_audit", "quill_sessions"} {
		if columns(t, pool, table) == "" {
			t.Fatalf("%s was not created", table)
		}
	}
}

func TestUpUpgradesAPreMigrationSchema(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `CREATE TABLE quill_threads (
		thread_id TEXT PRIMARY KEY, cwd TEXT NOT NULL, title TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, message_count INTEGER NOT NULL DEFAULT 0);
		INSERT INTO quill_threads (thread_id, cwd, created_at, updated_at) VALUES ('old', '/repo', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool, Quill()); err != nil {
		t.Fatal(err)
	}
	if got := columns(t, pool, "quill_threads"); !strings.Contains(got, "owner") || !strings.Contains(got, "workspace_id") {
		t.Fatalf("quill_threads columns = %s", got)
	}
	var cwd string
	if err := pool.QueryRow(ctx, `SELECT cwd FROM quill_threads WHERE thread_id = 'old'`).Scan(&cwd); err != nil || cwd != "/repo" {
		t.Fatalf("old row = %q, %v", cwd, err)
	}
}

func TestUpRefusesANewerDatabase(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	if err := Up(ctx, pool, Quill()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO quill_schema_migrations (version, name) VALUES (9999, 'future')`); err != nil {
		t.Fatal(err)
	}
	if err := Up(ctx, pool, Quill()); err == nil || !strings.Contains(err.Error(), "upgrade quill") {
		t.Fatalf("Up on a newer database = %v", err)
	}
}

func TestUpRollsBackAFailedMigration(t *testing.T) {
	pool := schemaPool(t)
	ctx := context.Background()
	migs := []Migration{
		{Version: 1, Name: "ok", SQL: "CREATE TABLE a (id INT)"},
		{Version: 2, Name: "broken", SQL: "CREATE TABLE b (id INT); SELECT nope FROM nowhere"},
	}
	if err := Up(ctx, pool, migs); err == nil || !strings.Contains(err.Error(), "0002_broken") {
		t.Fatalf("Up = %v", err)
	}
	if v, _ := Version(ctx, pool); v != 1 {
		t.Fatalf("version after failure = %d, want 1", v)
	}
	if columns(t, pool, "b") != "" {
		t.Fatal("the failed migration left table b behind")
	}
}

func TestConcurrentUpIsSerialized(t *testing.T) {
	pool := schemaPool(t)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = Up(context.Background(), pool, Quill())
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
