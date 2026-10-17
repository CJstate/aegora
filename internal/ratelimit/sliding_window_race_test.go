package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlidingWindowConcurrent(t *testing.T) {
	const maxRequests = 50
	const numGoroutines = 100
	const burstPerWorker = 10

	sw := NewSlidingWindow(10*time.Second, maxRequests)

	var admitted atomic.Int64
	var wg sync.WaitGroup

	ctx := context.Background()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < burstPerWorker; j++ {
				if sw.Allow(ctx) {
					admitted.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	if got := admitted.Load(); got != int64(maxRequests) {
		t.Fatalf("expected exactly %d total admissions under concurrent contention, got %d", maxRequests, got)
	}
}
