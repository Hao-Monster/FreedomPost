// Package ratelimit provides Redis-backed fixed-window rate limiting with
// in-process memory fallback. Exactly matches the implementation in
// services/paid-access/internal/ratelimit/limiter.go.
package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limiter is the rate limiting interface.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// ─── Redis limiter ────────────────────────────────────────────────────────────

// Redis implements fixed-window rate limiting using a Lua script for
// atomicity. Falls back to in-memory limiting during Redis outages.
type Redis struct {
	client   *redis.Client
	prefix   string
	fallback *Memory
}

// NewRedis creates a rate limiter backed by the given Redis URL.
// keyPrefix is prepended to all keys (e.g. "fp:api:").
func NewRedis(redisURL, keyPrefix string) (*Redis, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &Redis{
		client:   redis.NewClient(opts),
		prefix:   keyPrefix,
		fallback: NewMemory(),
	}, nil
}

// fixedWindowScript increments the counter and sets an expiry on first use.
// Identical to the paid-access Lua script.
var fixedWindowScript = redis.NewScript(`
local value = redis.call('INCR', KEYS[1])
if value == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return value
`)

// Allow returns true if the request is within the rate limit.
// On Redis error it falls back to in-memory limiting (strict, preserves protection).
func (r *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	fullKey := r.prefix + key
	value, err := fixedWindowScript.Run(
		ctx, r.client, []string{fullKey}, window.Milliseconds(),
	).Int()
	if err != nil {
		// Redis unavailable: use strict in-process fallback
		allowed, _ := r.fallback.Allow(ctx, key, limit, window)
		return allowed, err
	}
	return value <= limit, nil
}

func (r *Redis) Ping(ctx context.Context) error { return r.client.Ping(ctx).Err() }
func (r *Redis) Close() error                   { return r.client.Close() }

// ─── Memory limiter ───────────────────────────────────────────────────────────

type memBucket struct {
	started time.Time
	count   int
}

// Memory is a simple in-process fixed-window rate limiter.
// Used as fallback when Redis is unavailable.
type Memory struct {
	mu      sync.Mutex
	buckets map[string]memBucket
	now     func() time.Time
}

// NewMemory creates an in-process memory rate limiter.
func NewMemory() *Memory {
	return &Memory{
		buckets: make(map[string]memBucket),
		now:     time.Now,
	}
}

// NewMemoryWithClock creates a Memory rate limiter with a custom clock.
// Used in tests to control time.
func NewMemoryWithClock(clock func() time.Time) *Memory {
	return &Memory{
		buckets: make(map[string]memBucket),
		now:     clock,
	}
}

// SetClock replaces the clock used by the limiter. Used in tests only.
func (m *Memory) SetClock(clock func() time.Time) {
	m.mu.Lock()
	m.now = clock
	m.mu.Unlock()
}

func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	b := m.buckets[key]
	if b.started.IsZero() || now.Sub(b.started) >= window {
		b = memBucket{started: now}
	}
	b.count++
	m.buckets[key] = b
	// Evict stale entries to prevent unbounded growth (matches paid-access)
	if len(m.buckets) > 10_000 {
		for k, v := range m.buckets {
			if now.Sub(v.started) >= window {
				delete(m.buckets, k)
			}
		}
	}
	return b.count <= limit, nil
}
