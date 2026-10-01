package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStoreOptions configures a RedisStore.
type RedisStoreOptions struct {
	// Prefix starts every key, separating stores that share a database;
	// it must not contain "|". Default "deepagents".
	Prefix string
	// TTL expires each item this long after its last Put; 0 keeps items
	// until deleted.
	TTL time.Duration
}

// RedisStore keeps items as JSON strings in Redis, for data that can be
// regenerated (caches, offloaded tool output).
type RedisStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisStore returns a store on client. The caller owns the client.
func NewRedisStore(client *redis.Client, opts RedisStoreOptions) *RedisStore {
	if opts.Prefix == "" {
		opts.Prefix = "deepagents"
	}
	return &RedisStore{client: client, prefix: opts.Prefix, ttl: opts.TTL}
}

type redisItem struct {
	Value     map[string]any `json:"value"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// redisKey is "<prefix>|<namespace>|<key>"; namespaces cannot contain "|",
// so the key may hold anything.
func (s *RedisStore) redisKey(ns Namespace, key string) string {
	return s.prefix + "|" + joinNamespace(ns) + "|" + key
}

func (s *RedisStore) Get(ctx context.Context, ns Namespace, key string) (*Item, error) {
	if err := ValidateNamespace(ns); err != nil {
		return nil, err
	}
	raw, err := s.client.Get(ctx, s.redisKey(ns, key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get %s/%s: %w", joinNamespace(ns), key, err)
	}
	return s.decode(ns, key, raw)
}

func (s *RedisStore) decode(ns Namespace, key string, raw []byte) (*Item, error) {
	var ri redisItem
	if err := json.Unmarshal(raw, &ri); err != nil {
		return nil, fmt.Errorf("store: decode %s/%s: %w", joinNamespace(ns), key, err)
	}
	return &Item{Namespace: ns, Key: key, Value: ri.Value, CreatedAt: ri.CreatedAt, UpdatedAt: ri.UpdatedAt}, nil
}

func (s *RedisStore) Put(ctx context.Context, ns Namespace, key string, value map[string]any) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	now := time.Now()
	ri := redisItem{Value: value, CreatedAt: now, UpdatedAt: now}
	if prev, err := s.Get(ctx, ns, key); err == nil && prev != nil {
		ri.CreatedAt = prev.CreatedAt
	}
	raw, err := json.Marshal(ri)
	if err != nil {
		return fmt.Errorf("store: encode value: %w", err)
	}
	if err := s.client.Set(ctx, s.redisKey(ns, key), raw, s.ttl).Err(); err != nil {
		return fmt.Errorf("store: put %s/%s: %w", joinNamespace(ns), key, err)
	}
	return nil
}

func (s *RedisStore) Delete(ctx context.Context, ns Namespace, key string) error {
	if err := ValidateNamespace(ns); err != nil {
		return err
	}
	if err := s.client.Del(ctx, s.redisKey(ns, key)).Err(); err != nil {
		return fmt.Errorf("store: delete %s/%s: %w", joinNamespace(ns), key, err)
	}
	return nil
}

// Search scans the keys under prefix; it suits the small, per-project sets
// kept here, not large keyspaces.
func (s *RedisStore) Search(ctx context.Context, prefix Namespace, opts SearchOptions) ([]Item, error) {
	if err := checkSearch(prefix, opts); err != nil {
		return nil, err
	}
	pattern := escapeGlob(s.prefix+"|") + "*"
	if len(prefix) > 0 {
		pattern = escapeGlob(s.prefix+"|"+joinNamespace(prefix)) + "*"
	}
	var keys []string
	iter := s.client.Scan(ctx, 0, pattern, 500).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("store: search %s: %w", joinNamespace(prefix), err)
	}
	var out []Item
	for start := 0; start < len(keys); start += 500 {
		batch := keys[start:min(start+500, len(keys))]
		vals, err := s.client.MGet(ctx, batch...).Result()
		if err != nil {
			return nil, fmt.Errorf("store: search %s: %w", joinNamespace(prefix), err)
		}
		for i, v := range vals {
			raw, ok := v.(string)
			if !ok {
				continue // expired between SCAN and MGET
			}
			ns, key, ok := s.parseKey(batch[i])
			if !ok || !hasNamespacePrefix(ns, prefix) {
				continue
			}
			it, err := s.decode(ns, key, []byte(raw))
			if err != nil {
				return nil, err
			}
			out = append(out, *it)
		}
	}
	return finishSearch(out, opts), nil
}

func (s *RedisStore) parseKey(k string) (Namespace, string, bool) {
	rest, ok := strings.CutPrefix(k, s.prefix+"|")
	if !ok {
		return nil, "", false
	}
	ns, key, ok := strings.Cut(rest, "|")
	if !ok {
		return nil, "", false
	}
	return splitNamespace(ns), key, true
}

func escapeGlob(s string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`).Replace(s)
}
