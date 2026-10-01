package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// testStore runs the behavior every Store implementation must share. ns
// is unique per call so shared databases do not interfere.
func testStore(t *testing.T, s Store, ns Namespace) {
	ctx := context.Background()
	t.Run("missing key", func(t *testing.T) {
		it, err := s.Get(ctx, ns, "nope")
		if err != nil || it != nil {
			t.Fatalf("Get = %v, %v; want nil, nil", it, err)
		}
	})
	t.Run("put get keeps created_at", func(t *testing.T) {
		if err := s.Put(ctx, ns, "a", map[string]any{"content": "one"}); err != nil {
			t.Fatal(err)
		}
		first, err := s.Get(ctx, ns, "a")
		if err != nil || first == nil {
			t.Fatalf("Get = %v, %v", first, err)
		}
		if first.Value["content"] != "one" || first.Key != "a" || strings.Join(first.Namespace, ".") != strings.Join(ns, ".") {
			t.Fatalf("item = %+v", first)
		}
		time.Sleep(5 * time.Millisecond)
		if err := s.Put(ctx, ns, "a", map[string]any{"content": "two"}); err != nil {
			t.Fatal(err)
		}
		second, _ := s.Get(ctx, ns, "a")
		if second.Value["content"] != "two" {
			t.Fatalf("value = %v", second.Value)
		}
		if !second.CreatedAt.Equal(first.CreatedAt) || !second.UpdatedAt.After(first.UpdatedAt) {
			t.Fatalf("times: first %v/%v second %v/%v", first.CreatedAt, first.UpdatedAt, second.CreatedAt, second.UpdatedAt)
		}
	})
	t.Run("search by prefix, filter and paging", func(t *testing.T) {
		sub := append(append(Namespace{}, ns...), "sub")
		other := append(append(Namespace{}, ns[:len(ns)-1]...), ns[len(ns)-1]+"x")
		for _, put := range []struct {
			ns  Namespace
			key string
			v   map[string]any
		}{
			{ns, "b", map[string]any{"kind": "x"}},
			{sub, "c", map[string]any{"kind": "y"}},
			{other, "d", map[string]any{"kind": "x"}},
			{ns, "path/with *[?] chars", map[string]any{"kind": "x"}},
		} {
			if err := s.Put(ctx, put.ns, put.key, put.v); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_ = s.Delete(ctx, other, "d")
		})
		all, err := s.Search(ctx, ns, SearchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := keys(all); got != "a,b,path/with *[?] chars,c" {
			t.Fatalf("keys = %s", got)
		}
		xs, err := s.Search(ctx, ns, SearchOptions{Filter: map[string]any{"kind": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := keys(xs); got != "b,path/with *[?] chars" {
			t.Fatalf("filtered keys = %s", got)
		}
		page, err := s.Search(ctx, ns, SearchOptions{Limit: 2, Offset: 1})
		if err != nil {
			t.Fatal(err)
		}
		if got := keys(page); got != "b,path/with *[?] chars" {
			t.Fatalf("page keys = %s", got)
		}
		if _, err := s.Search(ctx, ns, SearchOptions{Query: "semantic"}); err == nil {
			t.Fatal("semantic query must be rejected")
		}
	})
	t.Run("delete", func(t *testing.T) {
		if err := s.Delete(ctx, ns, "a"); err != nil {
			t.Fatal(err)
		}
		if it, _ := s.Get(ctx, ns, "a"); it != nil {
			t.Fatalf("deleted item still there: %+v", it)
		}
		if err := s.Delete(ctx, ns, "never"); err != nil {
			t.Fatalf("deleting a missing key: %v", err)
		}
	})
	t.Run("invalid namespace", func(t *testing.T) {
		for _, bad := range []Namespace{{}, {""}, {"a.b"}, {"a", "b|c"}} {
			if err := s.Put(ctx, bad, "k", map[string]any{}); err == nil {
				t.Fatalf("Put(%q) must fail", bad)
			}
		}
	})
}

func keys(items []Item) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Key
	}
	return strings.Join(out, ",")
}

func uniqueNS(name string) Namespace {
	return Namespace{"test", fmt.Sprintf("%s%d", name, time.Now().UnixNano())}
}

func TestInMemoryStore(t *testing.T) {
	testStore(t, NewInMemoryStore(), uniqueNS("mem"))
}

func TestPostgresStore(t *testing.T) {
	conn := os.Getenv("POSTGRES_CONN_STRING")
	if conn == "" {
		t.Skip("POSTGRES_CONN_STRING is not set")
	}
	cp, err := NewPostgresCheckpointer(context.Background(), conn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cp.Close)
	s, err := NewPostgresStore(context.Background(), cp.Pool())
	if err != nil {
		t.Fatal(err)
	}
	ns := uniqueNS("pg")
	t.Cleanup(func() { deleteAll(t, s, ns) })
	testStore(t, s, ns)
}

func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL is not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRedisStore(t *testing.T) {
	c := newTestRedis(t)
	s := NewRedisStore(c, RedisStoreOptions{Prefix: "deepagents-test", TTL: time.Minute})
	ns := uniqueNS("redis")
	t.Cleanup(func() { deleteAll(t, s, ns) })
	testStore(t, s, ns)
}

func TestRedisStoreExpires(t *testing.T) {
	c := newTestRedis(t)
	s := NewRedisStore(c, RedisStoreOptions{Prefix: "deepagents-test", TTL: time.Second})
	ns := uniqueNS("ttl")
	ctx := context.Background()
	if err := s.Put(ctx, ns, "k", map[string]any{"v": 1}); err != nil {
		t.Fatal(err)
	}
	ttl, err := c.TTL(ctx, s.redisKey(ns, "k")).Result()
	if err != nil || ttl <= 0 || ttl > time.Second {
		t.Fatalf("ttl = %v, %v", ttl, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if it, _ := s.Get(ctx, ns, "k"); it != nil {
		t.Fatalf("expired item still readable: %+v", it)
	}
}

func deleteAll(t *testing.T, s Store, ns Namespace) {
	items, err := s.Search(context.Background(), ns, SearchOptions{})
	if err != nil {
		t.Logf("cleanup: %v", err)
		return
	}
	for _, it := range items {
		_ = s.Delete(context.Background(), it.Namespace, it.Key)
	}
}
