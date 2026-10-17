package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestSlidingWindow_CapacityRespected(t *testing.T) {
	ctx := context.Background()
	sw := NewSlidingWindow(time.Minute, 3)

	if sw.Limit() != 3 {
		t.Fatalf("expected limit 3, got %d", sw.Limit())
	}

	for i := 0; i < 3; i++ {
		if !sw.Allow(ctx) {
			t.Fatalf("expected request %d to be allowed", i+1)
		}
	}

	if sw.Allow(ctx) {
		t.Fatal("expected 4th request to be denied")
	}

	if rem := sw.Remaining(); rem != 0 {
		t.Fatalf("expected 0 remaining requests, got %d", rem)
	}
}

func TestSlidingWindow_RefillAfterWindow(t *testing.T) {
	ctx := context.Background()
	window := 50 * time.Millisecond
	sw := NewSlidingWindow(window, 2)

	if !sw.Allow(ctx) {
		t.Fatal("expected first request to be allowed")
	}
	if !sw.Allow(ctx) {
		t.Fatal("expected second request to be allowed")
	}

	if sw.Allow(ctx) {
		t.Fatal("expected 3rd request to be denied")
	}

	time.Sleep(window + 10*time.Millisecond)

	if !sw.Allow(ctx) {
		t.Fatal("expected request after window expiration to be allowed")
	}
}

func TestSlidingWindow_SlidingProperty(t *testing.T) {
	ctx := context.Background()
	// window 200ms, max 2:
	// admit at t=0, sleep 150ms, admit, sleep 60ms -> exactly one more admit then deny; this proves no fixed-window reset
	sw := NewSlidingWindow(200*time.Millisecond, 2)

	// 1. Admit at t=0
	if !sw.Allow(ctx) {
		t.Fatal("expected first request at t=0 to be allowed")
	}

	// 2. Sleep 150ms and admit second request
	time.Sleep(150 * time.Millisecond)
	if !sw.Allow(ctx) {
		t.Fatal("expected second request at t=150ms to be allowed")
	}

	// Currently at capacity (2 requests in window)
	if sw.Allow(ctx) {
		t.Fatal("expected request at t=150ms to be denied when full")
	}

	// 3. Sleep 60ms (total elapsed ~210ms > 200ms window from first request, but < 200ms from second request)
	time.Sleep(60 * time.Millisecond)

	// Exactly one more admit should succeed (first request expired, second request still active)
	if !sw.Allow(ctx) {
		t.Fatal("expected exactly one more admit after first request expired")
	}

	// The next immediate request must be denied (now 2 requests active: t=150ms and t=210ms)
	if sw.Allow(ctx) {
		t.Fatal("expected immediate subsequent request to be denied, proving sliding window semantics")
	}
}

func TestSlidingWindow_ConstructorPanics(t *testing.T) {
	tests := []struct {
		name        string
		window      time.Duration
		maxRequests int
	}{
		{
			name:        "zero window",
			window:      0,
			maxRequests: 10,
		},
		{
			name:        "negative window",
			window:      -time.Second,
			maxRequests: 10,
		},
		{
			name:        "zero maxRequests",
			window:      time.Second,
			maxRequests: 0,
		},
		{
			name:        "negative maxRequests",
			window:      time.Second,
			maxRequests: -5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected NewSlidingWindow to panic on non-positive arguments")
				}
			}()

			_ = NewSlidingWindow(tt.window, tt.maxRequests)
		})
	}
}

func TestSlidingWindow_RetryAfter(t *testing.T) {
	ctx := context.Background()
	window := 100 * time.Millisecond
	sw := NewSlidingWindow(window, 1)

	// When below capacity, RetryAfter should return 0
	if retry := sw.RetryAfter(); retry != 0 {
		t.Fatalf("expected 0 RetryAfter when below capacity, got %v", retry)
	}

	// Exhaust capacity
	if !sw.Allow(ctx) {
		t.Fatal("expected request to be allowed")
	}

	// When at capacity, RetryAfter should return remaining duration > 0 and <= window
	retry := sw.RetryAfter()
	if retry <= 0 || retry > window {
		t.Fatalf("expected 0 < RetryAfter <= %v, got %v", window, retry)
	}

	time.Sleep(window + 10*time.Millisecond)

	// After expiration, RetryAfter should return 0 again
	if retry = sw.RetryAfter(); retry != 0 {
		t.Fatalf("expected 0 RetryAfter after expiration, got %v", retry)
	}
}
