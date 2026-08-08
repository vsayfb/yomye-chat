package ratelimit

import (
	"testing"
	"time"
)

func TestLimiterAllowsBurstThenRefills(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	limiter := newLimiter(60, time.Minute, 2, func() time.Time { return now })

	if ok, _ := limiter.Allow("user"); !ok {
		t.Fatal("first request denied")
	}
	if ok, _ := limiter.Allow("user"); !ok {
		t.Fatal("second burst request denied")
	}
	if ok, retry := limiter.Allow("user"); ok || retry <= 0 {
		t.Fatalf("third request = (%v, %v), want denied with retry", ok, retry)
	}

	now = now.Add(time.Second)
	if ok, _ := limiter.Allow("user"); !ok {
		t.Fatal("request was not allowed after one-token refill")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	now := time.Now()
	limiter := newLimiter(1, time.Minute, 1, func() time.Time { return now })

	if ok, _ := limiter.Allow("alice"); !ok {
		t.Fatal("alice first request denied")
	}
	if ok, _ := limiter.Allow("alice"); ok {
		t.Fatal("alice second request unexpectedly allowed")
	}
	if ok, _ := limiter.Allow("bob"); !ok {
		t.Fatal("bob was affected by alice's bucket")
	}
}

func TestLimiterEvictsIdleBuckets(t *testing.T) {
	now := time.Now()
	limiter := newLimiter(1, time.Minute, 1, func() time.Time { return now })
	limiter.idleTTL = time.Minute
	limiter.cleanupInterval = time.Second

	_, _ = limiter.Allow("old")
	now = now.Add(2 * time.Minute)
	_, _ = limiter.Allow("new")

	if _, ok := limiter.buckets["old"]; ok {
		t.Fatal("idle bucket was not evicted")
	}
}
