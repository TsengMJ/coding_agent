package cache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testCache(t *testing.T, c Cache) {
	ctx := context.Background()
	key := fmt.Sprintf("k%d", time.Now().UnixNano())
	if _, ok, err := c.Get(ctx, key); ok || err != nil {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := c.Set(ctx, key, []byte("v1"), time.Minute); err != nil {
		t.Fatal(err)
	}
	v, ok, err := c.Get(ctx, key)
	if err != nil || !ok || string(v) != "v1" {
		t.Fatalf("get = %q %v %v", v, ok, err)
	}
	short := key + "-short"
	if err := c.Set(ctx, short, []byte("v2"), time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, ok, _ := c.Get(ctx, short); ok {
		t.Fatal("an expired entry must not be returned")
	}
}

func TestMemory(t *testing.T) { testCache(t, NewMemory()) }

func TestRedis(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL is not set")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opt)
	t.Cleanup(func() { _ = client.Close() })
	testCache(t, NewRedis(client, "quill-test"))
}

func TestKeyIsStableAndShort(t *testing.T) {
	a := Key("web", "https://example.com", "what is it")
	if a != Key("web", "https://example.com", "what is it") {
		t.Fatal("same parts must give the same key")
	}
	if a == Key("web", "https://example.com/", "what is it") || a == Key("web", "https://example.com", "what") {
		t.Fatal("different parts must give different keys")
	}
	if Key("web", "a|b") == Key("web", "a", "b") {
		t.Fatal("part boundaries must matter")
	}
}
