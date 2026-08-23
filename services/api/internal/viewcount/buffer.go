// Package viewcount implements an asynchronous view count buffer.
// Writes go to Redis INCR, a background goroutine flushes to PostgreSQL
// every 30 seconds, reducing hot-path DB writes for popular posts.
package viewcount

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const viewKeyPrefix = "fp:viewcount:"

// Flusher is the interface required to flush counts to the database.
type Flusher interface {
	// IncrementViewCount adds delta views to a post by slug, returning the new total.
	IncrementViewCount(ctx context.Context, postSlug string, delta int) (int, error)
}

// Buffer accumulates view increments in Redis and periodically flushes to DB.
type Buffer struct {
	redis   *redis.Client
	flusher Flusher
	logger  *slog.Logger
}

// NewBuffer creates a new view count buffer.
func NewBuffer(redisClient *redis.Client, flusher Flusher, logger *slog.Logger) *Buffer {
	return &Buffer{redis: redisClient, flusher: flusher, logger: logger}
}

// Record atomically increments the in-Redis view counter for a post.
// This is the hot path: O(1) Redis INCR, no DB write.
func (b *Buffer) Record(ctx context.Context, postSlug string) error {
	return b.redis.Incr(ctx, viewKeyPrefix+postSlug).Err()
}

// StartFlusher starts a background goroutine that flushes accumulated counts
// to the database at the specified interval. Call with go b.StartFlusher(...).
func (b *Buffer) StartFlusher(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Final flush on shutdown
			b.flush(context.Background())
			return
		case <-ticker.C:
			b.flush(ctx)
		}
	}
}

// Flush forces an immediate flush of all pending counts. Call on graceful shutdown.
func (b *Buffer) Flush(ctx context.Context) {
	b.flush(ctx)
}

func (b *Buffer) flush(ctx context.Context) {
	// Scan for all view count keys
	var cursor uint64
	var keys []string
	for {
		var batch []string
		var err error
		batch, cursor, err = b.redis.Scan(ctx, cursor, viewKeyPrefix+"*", 100).Result()
		if err != nil {
			b.logger.Error("viewcount flush: scan redis", "error", err)
			return
		}
		keys = append(keys, batch...)
		if cursor == 0 {
			break
		}
	}

	for _, key := range keys {
		slug := key[len(viewKeyPrefix):]
		// GETDEL: atomically read and delete the counter
		val, err := b.redis.GetDel(ctx, key).Int()
		if err == redis.Nil || val == 0 {
			continue
		}
		if err != nil {
			b.logger.Error("viewcount flush: getdel", "key", key, "error", err)
			continue
		}
		if _, err := b.flusher.IncrementViewCount(ctx, slug, val); err != nil {
			b.logger.Error("viewcount flush: increment db", "slug", slug, "delta", val, "error", err)
			// Re-add to Redis so the count is not lost
			if restoreErr := b.redis.IncrBy(ctx, key, int64(val)).Err(); restoreErr != nil {
				b.logger.Error("viewcount flush: restore to redis", "slug", slug, "error", restoreErr)
			}
		}
	}
}

// ErrKeyMissing is returned when a Redis key does not exist.
var ErrKeyMissing = fmt.Errorf("redis key not found")
