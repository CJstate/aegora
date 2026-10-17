package ratelimit

import (
	"context"
	"testing"
	"time"
)

// BenchmarkSlidingWindowAllow measures the throughput and concurrency overhead of
// SlidingWindow.Allow() under parallel goroutine contention.
func BenchmarkSlidingWindowAllow(b *testing.B) {
	// Pre-allocate a large window capacity so capacity checks focus on lock contention
	// and timestamp recording rather than hitting arbitrary rate limit rejections.
	sw := NewSlidingWindow(time.Minute, 10_000_000)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = sw.Allow(ctx)
		}
	})
}
