// Package cache holds short-lived results that can be fetched again (web
// pages, search results, documentation lookups) so repeated tool calls do
// not repeat network requests or model calls.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache stores byte values that expire after a per-entry TTL.
type Cache interface {
	// Get returns ok == false for a missing or expired key.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
}

// Key builds a cache key from a kind and the parts that identify a result;
// the parts are hashed so keys stay short whatever the input.
func Key(kind string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strconv.Itoa(len(p))))
		h.Write([]byte{':'})
		h.Write([]byte(p))
	}
	return kind + ":" + hex.EncodeToString(h.Sum(nil)[:16])
}

// Redis is a Cache on a Redis client.
type Redis struct {
	client *redis.Client
	prefix string
}

// NewRedis returns a cache whose keys start with prefix + ":". The caller
// owns the client.
func NewRedis(client *redis.Client, prefix string) *Redis {
	return &Redis{client: client, prefix: prefix}
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := r.client.Get(ctx, r.prefix+":"+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("cache: get %s: %w", key, err)
	}
	return v, true, nil
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if err := r.client.Set(ctx, r.prefix+":"+key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache: set %s: %w", key, err)
	}
	return nil
}

// Memory is an in-process Cache for tests.
type Memory struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	value   []byte
	expires time.Time
}

// NewMemory returns an empty in-process cache.
func NewMemory() *Memory { return &Memory{entries: map[string]memoryEntry{}} }

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || (!e.expires.IsZero() && time.Now().After(e.expires)) {
		delete(m.entries, key)
		return nil, false, nil
	}
	return append([]byte(nil), e.value...), true, nil
}

func (m *Memory) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := memoryEntry{value: append([]byte(nil), value...)}
	if ttl > 0 {
		e.expires = time.Now().Add(ttl)
	}
	m.entries[key] = e
	return nil
}
