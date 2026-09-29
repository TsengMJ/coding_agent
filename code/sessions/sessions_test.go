package sessions

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNewThreadIDIsUniqueHex(t *testing.T) {
	a, b := NewThreadID(), NewThreadID()
	if len(a) != 12 || a == b {
		t.Fatalf("ids = %q, %q", a, b)
	}
}

func TestTitleUsesFirstLineAndTruncates(t *testing.T) {
	if got := Title("  fix the bug\nmore"); got != "fix the bug" {
		t.Fatalf("title = %q", got)
	}
	long := strings.Repeat("字", 70)
	if got := Title(long); got != strings.Repeat("字", 60)+"..." {
		t.Fatalf("long title = %q", got)
	}
}

func testStore(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	id := func(n string) string { return t.Name() + "-" + n }
	p, q := "/"+t.Name()+"/p", "/"+t.Name()+"/q"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, n := range []string{"a", "b", "c"} {
			_ = s.Delete(context.Background(), id(n))
		}
	})
	must(s.Touch(ctx, Info{ThreadID: id("a"), Cwd: p, Title: "first", UpdatedAt: base}))
	must(s.Touch(ctx, Info{ThreadID: id("b"), Cwd: p, Title: "second", UpdatedAt: base.Add(time.Minute)}))
	must(s.Touch(ctx, Info{ThreadID: id("c"), Cwd: q, Title: "other", UpdatedAt: base.Add(2 * time.Minute)}))
	must(s.Touch(ctx, Info{ThreadID: id("a"), Cwd: p + "moved", Title: "renamed", MessageCount: 4, UpdatedAt: base.Add(3 * time.Minute)}))

	got, err := s.Get(ctx, id("a"))
	must(err)
	if got == nil || got.Cwd != p || got.Title != "first" || got.MessageCount != 4 || !got.CreatedAt.Equal(base) {
		t.Fatalf("a = %+v", got)
	}
	list, err := s.List(ctx, Filter{Cwd: p})
	must(err)
	if len(list) != 2 || list[0].ThreadID != id("a") || list[1].ThreadID != id("b") {
		t.Fatalf("list = %+v", list)
	}
	latest, err := Latest(ctx, s, q)
	must(err)
	if latest == nil || latest.ThreadID != id("c") {
		t.Fatalf("latest = %+v", latest)
	}
	must(s.Delete(ctx, id("b")))
	if got, _ := s.Get(ctx, id("b")); got != nil {
		t.Fatalf("deleted thread still present: %+v", got)
	}
	if missing, err := s.Get(ctx, id("zzz")); err != nil || missing != nil {
		t.Fatalf("missing = %+v, %v", missing, err)
	}
}

func TestMemoryStore(t *testing.T) {
	testStore(t, NewMemoryStore())
}

func TestPostgresStore(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	pool, err := pgxpool.New(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s, err := NewPostgresStore(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	testStore(t, s)
}
