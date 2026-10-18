package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client   *redis.Client
	fallback *Memory
}

func NewRedis(rawURL string) (*Redis, error) {
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, err
	}
	return &Redis{client: redis.NewClient(options), fallback: NewMemory()}, nil
}

var fixedWindowScript = redis.NewScript(`
local value = redis.call('INCR', KEYS[1])
if value == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return value
`)

func (limiter *Redis) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	value, err := fixedWindowScript.Run(ctx, limiter.client, []string{"fp:paid-access:" + key}, window.Milliseconds()).Int()
	if err != nil {
		// A strict in-process limiter preserves protection during a transient Redis outage.
		allowed, _ := limiter.fallback.Allow(ctx, key, limit, window)
		return allowed, err
	}
	return value <= limit, nil
}

func (limiter *Redis) Ping(ctx context.Context) error { return limiter.client.Ping(ctx).Err() }
func (limiter *Redis) Close() error                   { return limiter.client.Close() }

type memoryBucket struct {
	started   time.Time
	expiresAt time.Time
	count     int
}

type Memory struct {
	mu      sync.Mutex
	buckets map[string]memoryBucket
	now     func() time.Time
}

func NewMemory() *Memory { return &Memory{buckets: make(map[string]memoryBucket), now: time.Now} }

func (limiter *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	bucket := limiter.buckets[key]
	if bucket.expiresAt.IsZero() || !now.Before(bucket.expiresAt) {
		bucket = memoryBucket{started: now, expiresAt: now.Add(window)}
	}
	bucket.count++
	limiter.buckets[key] = bucket
	if len(limiter.buckets) > 10_000 {
		for item, value := range limiter.buckets {
			if !now.Before(value.expiresAt) {
				delete(limiter.buckets, item)
			}
		}
	}
	return bucket.count <= limit, nil
}
