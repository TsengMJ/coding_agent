package storage

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tsengmingchun/deepagents_golang/middleware/planmode"
	"github.com/tsengmingchun/deepagents_golang/middleware/todolist"
	"github.com/tsengmingchun/deepagents_golang/runtime"
)

func TestOpenRequiresPostgres(t *testing.T) {
	_, err := Open(context.Background(), "", "redis://localhost:6379/0")
	if err == nil || !strings.Contains(err.Error(), "DCODE_POSTGRES_CONN_STRING") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRequiresRedis(t *testing.T) {
	_, err := Open(context.Background(), "postgres://x@localhost/x", "")
	if err == nil || !strings.Contains(err.Error(), "DCODE_REDIS_URL") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenFailsWhenRedisUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Open(ctx, "postgres://x@localhost/x", "redis://127.0.0.1:1/0")
	if err == nil || !strings.Contains(err.Error(), "Redis") {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryStorage(t *testing.T) {
	s := NewMemory()
	defer s.Close()
	if s.Kind != KindMemory || s.Expiring(time.Hour) == nil || s.Checkpointer == nil || s.Plans == nil {
		t.Fatalf("storage = %+v", s)
	}
}

func openReal(t *testing.T) *Storage {
	t.Helper()
	pg, rd := os.Getenv("POSTGRES_CONN_STRING"), os.Getenv("REDIS_URL")
	if pg == "" || rd == "" {
		t.Skip("POSTGRES_CONN_STRING and REDIS_URL not set")
	}
	s, err := Open(context.Background(), pg, rd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestOpenPostgresAndRedis(t *testing.T) {
	s := openReal(t)
	ctx := context.Background()
	if s.Kind != KindPostgres {
		t.Fatalf("kind = %q", s.Kind)
	}
	ns := runtime.Namespace{"storage-test", strconv.FormatInt(time.Now().UnixNano(), 10)}
	ex := s.Expiring(time.Minute)
	if err := ex.Put(ctx, ns, "k", map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ex.Delete(ctx, ns, "k") })
	if it, err := ex.Get(ctx, ns, "k"); err != nil || it == nil {
		t.Fatalf("expiring get = %+v, %v", it, err)
	}
	if err := s.Cache.Set(ctx, "storage-test", []byte("x"), time.Minute); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.Cache.Get(ctx, "storage-test"); err != nil || !ok || string(v) != "x" {
		t.Fatalf("cache get = %q %v %v", v, ok, err)
	}
}

func TestPostgresPlans(t *testing.T) {
	s := openReal(t)
	ctx := context.Background()
	plans := s.Plans.(*PostgresPlans)
	id := "storage-test-" + time.Now().Format("150405.000000")
	t.Cleanup(func() { _ = plans.Delete(ctx, id) })

	if p, err := plans.Get(ctx, id); err != nil || p != nil {
		t.Fatalf("missing plan = %+v, %v", p, err)
	}
	p := planmode.Plan{ID: id, Name: "First", Body: "body", Status: planmode.StatusDraft,
		Todos: []todolist.Todo{{ID: "a", Content: "do a", Status: todolist.StatusPending}}}
	if err := plans.Save(ctx, "t1", p); err != nil {
		t.Fatal(err)
	}
	p.Name, p.Status = "Second", planmode.StatusBuilding
	if err := plans.Save(ctx, "t1", p); err != nil {
		t.Fatal(err)
	}
	got, err := plans.Get(ctx, id)
	if err != nil || got == nil || got.Name != "Second" || got.Status != planmode.StatusBuilding || len(got.Todos) != 1 {
		t.Fatalf("plan = %+v, %v", got, err)
	}
}
