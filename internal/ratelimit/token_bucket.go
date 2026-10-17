package ratelimit

import (
	"context"
	"sync"
	"time"
)

// TokenBucket implements an in-memory token bucket rate limiter.
type TokenBucket struct {
	mu sync.Mutex

	capacity float64
	tokens   float64
	rate     float64

	lastRefill time.Time
}

// NewTokenBucket creates a token bucket with the specified capacity
// and refill rate expressed in tokens per second.
func NewTokenBucket(capacity int, refillPerSecond float64) *TokenBucket {
	return &TokenBucket{
		capacity:   float64(capacity),
		tokens:     float64(capacity),
		rate:       refillPerSecond,
		lastRefill: time.Now(),
	}
}

// Allow reports whether a request is permitted by the rate limiter.
func (tb *TokenBucket) Allow(context.Context) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()

	elapsed := now.Sub(tb.lastRefill).Seconds()

	tb.tokens += elapsed * tb.rate

	if tb.tokens > tb.capacity {
		tb.tokens = tb.capacity
	}

	tb.lastRefill = now

	if tb.tokens < 1 {
		return false
	}

	tb.tokens--

	return true
}

// Limit returns the maximum token capacity.
func (tb *TokenBucket) Limit() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return int(tb.capacity)
}

// Remaining returns the estimated number of available tokens.
// Best-effort under concurrency.
func (tb *TokenBucket) Remaining() int {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tokens := tb.tokens + elapsed*tb.rate
	if tokens > tb.capacity {
		tokens = tb.capacity
	}
	if tokens < 0 {
		return 0
	}
	return int(tokens)
}
