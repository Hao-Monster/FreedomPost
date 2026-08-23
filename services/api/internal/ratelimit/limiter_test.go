package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/fenghaoyun-monster/freedompost/services/api/internal/ratelimit"
)

func TestMemoryAllow_BasicWindow(t *testing.T) {
	t.Parallel()
	m := ratelimit.NewMemory()
	ctx := context.Background()

	// First 3 allowed
	for i := 1; i <= 3; i++ {
		ok, err := m.Allow(ctx, "test", 3, time.Minute)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("attempt %d should be allowed", i)
		}
	}

	// 4th denied
	ok, _ := m.Allow(ctx, "test", 3, time.Minute)
	if ok {
		t.Fatal("4th attempt should be denied")
	}
}

func TestMemoryAllow_WindowReset(t *testing.T) {
	t.Parallel()
	now := time.Now()
	m := &ratelimit.Memory{}
	m = ratelimit.NewMemoryWithClock(func() time.Time { return now })
	ctx := context.Background()

	// Fill up the window
	for i := 0; i < 2; i++ {
		m.Allow(ctx, "key", 2, time.Second)
	}
	ok, _ := m.Allow(ctx, "key", 2, time.Second)
	if ok {
		t.Fatal("should be denied after limit")
	}

	// Advance past the window
	m.SetClock(func() time.Time { return now.Add(2 * time.Second) })
	ok, _ = m.Allow(ctx, "key", 2, time.Second)
	if !ok {
		t.Fatal("should be allowed after window reset")
	}
}

func TestMemoryAllow_MultipleKeys(t *testing.T) {
	t.Parallel()
	m := ratelimit.NewMemory()
	ctx := context.Background()

	// Different keys are independent
	for _, key := range []string{"a", "b", "c"} {
		for i := 0; i < 3; i++ {
			ok, _ := m.Allow(ctx, key, 3, time.Minute)
			if !ok {
				t.Errorf("key=%s attempt=%d should be allowed", key, i+1)
			}
		}
	}
}

func TestMemoryAllow_Invariants(t *testing.T) {
	t.Parallel()
	m := ratelimit.NewMemory()
	ctx := context.Background()
	limit := 5

	var allowed, denied int
	for i := 0; i < 10; i++ {
		ok, _ := m.Allow(ctx, "inv", limit, time.Minute)
		if ok {
			allowed++
		} else {
			denied++
		}
	}
	if allowed != limit {
		t.Errorf("allowed=%d, want %d", allowed, limit)
	}
	if denied != 10-limit {
		t.Errorf("denied=%d, want %d", denied, 10-limit)
	}
}
