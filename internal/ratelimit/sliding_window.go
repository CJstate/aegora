package ratelimit

import (
	"context"
	"sync"
	"time"
)

var _ Limiter = (*SlidingWindow)(nil)

// SlidingWindow implements an in-memory sliding window rate limiter that tracks
// admission timestamps within a moving time window.
type SlidingWindow struct {
	mu          sync.Mutex
	window      time.Duration
	maxRequests int
	timestamps  []time.Time
}

// NewSlidingWindow creates a SlidingWindow rate limiter with the specified duration
// window and maximum allowed requests. It panics if window or maxRequests is non-positive.
func NewSlidingWindow(window time.Duration, maxRequests int) *SlidingWindow {
	if window <= 0 {
		panic("ratelimit: window must be positive")
	}
	if maxRequests <= 0 {
		panic("ratelimit: maxRequests must be positive")
	}

	return &SlidingWindow{
		window:      window,
		maxRequests: maxRequests,
		timestamps:  make([]time.Time, 0, maxRequests),
	}
}

// Allow reports whether a request is permitted by the sliding window rate limiter.
// Timestamps older than now - window are pruned before checking capacity.
func (sw *SlidingWindow) Allow(context.Context) bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	sw.prune(now)

	if len(sw.timestamps) >= sw.maxRequests {
		return false
	}

	sw.timestamps = append(sw.timestamps, now)
	return true
}

// RetryAfter returns the duration until the oldest admission expires, i.e.,
// when a new request would be permitted. Returns 0 if requests are currently allowed.
// Note that under concurrent access, this estimate is best-effort.
func (sw *SlidingWindow) RetryAfter() time.Duration {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	now := time.Now()
	sw.prune(now)

	if len(sw.timestamps) < sw.maxRequests {
		return 0
	}

	oldest := sw.timestamps[0]
	expiresAt := oldest.Add(sw.window)
	rem := expiresAt.Sub(now)
	if rem <= 0 {
		return 0
	}
	return rem
}

// Limit returns the maximum number of requests allowed within the window.
func (sw *SlidingWindow) Limit() int {
	return sw.maxRequests
}

// Remaining returns the estimated number of remaining requests permitted in the current window.
// Note that under concurrent access, this value is best-effort.
func (sw *SlidingWindow) Remaining() int {
	sw.mu.Lock()
	defer sw.mu.Unlock()

	sw.prune(time.Now())
	rem := sw.maxRequests - len(sw.timestamps)
	if rem < 0 {
		return 0
	}
	return rem
}

func (sw *SlidingWindow) prune(now time.Time) {
	cutoff := now.Add(-sw.window)
	i := 0
	for i < len(sw.timestamps) && sw.timestamps[i].Before(cutoff) {
		i++
	}
	if i > 0 {
		copy(sw.timestamps, sw.timestamps[i:])
		sw.timestamps = sw.timestamps[:len(sw.timestamps)-i]
	}
}
