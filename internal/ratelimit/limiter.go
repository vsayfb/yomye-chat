package ratelimit

import (
	"math"
	"sync"
	"time"
)

const (
	defaultIdleTTL         = 10 * time.Minute
	defaultCleanupInterval = time.Minute
)

type bucket struct {
	tokens   float64
	last     time.Time
	lastSeen time.Time
}

type Limiter struct {
	mu sync.Mutex

	ratePerSecond float64
	burst         float64
	buckets       map[string]*bucket

	idleTTL         time.Duration
	cleanupInterval time.Duration
	lastCleanup     time.Time
	now             func() time.Time
}

func New(requests int, interval time.Duration, burst int) *Limiter {
	return newLimiter(requests, interval, burst, time.Now)
}

func newLimiter(requests int, interval time.Duration, burst int, now func() time.Time) *Limiter {
	if requests <= 0 {
		panic("rate limiter requests must be positive")
	}
	if interval <= 0 {
		panic("rate limiter interval must be positive")
	}
	if burst <= 0 {
		panic("rate limiter burst must be positive")
	}
	if now == nil {
		panic("rate limiter clock must not be nil")
	}

	current := now()
	return &Limiter{
		ratePerSecond:   float64(requests) / interval.Seconds(),
		burst:           float64(burst),
		buckets:         make(map[string]*bucket),
		idleTTL:         defaultIdleTTL,
		cleanupInterval: defaultCleanupInterval,
		lastCleanup:     current,
		now:             now,
	}
}

// Allow consumes one token for key. retryAfter is non-zero only when the
// request is denied and indicates when one token is expected to be available.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	if key == "" {
		return false, time.Second
	}

	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.cleanup(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now, lastSeen: now}
		l.buckets[key] = b
	}

	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.ratePerSecond)
		b.last = now
	}
	b.lastSeen = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	seconds := (1 - b.tokens) / l.ratePerSecond
	if seconds <= 0 {
		seconds = 0.001
	}
	return false, time.Duration(seconds * float64(time.Second))
}

func (l *Limiter) cleanup(now time.Time) {
	if now.Sub(l.lastCleanup) < l.cleanupInterval {
		return
	}

	cutoff := now.Add(-l.idleTTL)
	for key, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
	l.lastCleanup = now
}
