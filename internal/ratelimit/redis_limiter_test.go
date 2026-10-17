package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisLimiter_DistributedBudgetSharing(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	const key = "user:distributed"
	const maxReqs = 2

	// Two separate limiter instances simulating two gateway replicas
	limiter1 := NewRedisLimiter(rdb, time.Minute, maxReqs)
	limiter2 := NewRedisLimiter(rdb, time.Minute, maxReqs)

	// Instance 1 consumes 1 token
	ok1, err1 := limiter1.AllowFor(ctx, key)
	if err1 != nil || !ok1 {
		t.Fatalf("limiter1 expected allow, got ok=%v err=%v", ok1, err1)
	}

	// Instance 2 consumes 2nd token
	ok2, err2 := limiter2.AllowFor(ctx, key)
	if err2 != nil || !ok2 {
		t.Fatalf("limiter2 expected allow, got ok=%v err=%v", ok2, err2)
	}

	// Both instances should now see the budget exhausted
	ok3, err3 := limiter1.AllowFor(ctx, key)
	if err3 != nil || ok3 {
		t.Fatalf("limiter1 expected deny when shared budget exhausted, got ok=%v err=%v", ok3, err3)
	}

	ok4, err4 := limiter2.AllowFor(ctx, key)
	if err4 != nil || ok4 {
		t.Fatalf("limiter2 expected deny when shared budget exhausted, got ok=%v err=%v", ok4, err4)
	}
}

func TestRedisLimiter_WindowExpiry(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	const key = "user:expiry"
	const window = 60 * time.Millisecond
	const maxReqs = 2

	limiter := NewRedisLimiter(rdb, window, maxReqs)

	// Exhaust budget
	for i := 0; i < maxReqs; i++ {
		ok, err := limiter.AllowFor(ctx, key)
		if err != nil || !ok {
			t.Fatalf("request %d expected allow, got ok=%v err=%v", i+1, ok, err)
		}
	}

	// Over budget
	ok, err := limiter.AllowFor(ctx, key)
	if err != nil || ok {
		t.Fatalf("expected deny when full, got ok=%v err=%v", ok, err)
	}

	// Sleep past the window duration
	time.Sleep(window + 20*time.Millisecond)

	// Budget should now refill
	okRefill, errRefill := limiter.AllowFor(ctx, key)
	if errRefill != nil || !okRefill {
		t.Fatalf("expected allow after window expiry, got ok=%v err=%v", okRefill, errRefill)
	}
}

func TestRedisLimiter_KeyNamespacing(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	limiter := NewRedisLimiter(rdb, time.Minute, 1)

	// Consume key A
	okA1, errA1 := limiter.AllowFor(ctx, "client-A")
	if errA1 != nil || !okA1 {
		t.Fatalf("client-A expected allow, got ok=%v err=%v", okA1, errA1)
	}

	// client-A exhausted
	okA2, errA2 := limiter.AllowFor(ctx, "client-A")
	if errA2 != nil || okA2 {
		t.Fatalf("client-A expected deny, got ok=%v err=%v", okA2, errA2)
	}

	// client-B has an independent budget and should be admitted
	okB1, errB1 := limiter.AllowFor(ctx, "client-B")
	if errB1 != nil || !okB1 {
		t.Fatalf("client-B expected allow, got ok=%v err=%v", okB1, errB1)
	}
}

func TestRedisLimiter_FailOpen(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})

	ctx := context.Background()
	const key = "user:failopen"

	limiter := NewRedisLimiter(rdb, time.Minute, 1)
	boundLimiter := limiter.ForKey(key)

	if lp, ok := boundLimiter.(interface{ Limit() int }); !ok || lp.Limit() != 1 {
		t.Fatalf("expected Limit() 1, got %v", boundLimiter)
	}

	// Stop miniredis server to simulate connection failure / outage
	s.Close()
	_ = rdb.Close()

	// Direct AllowFor returns an error
	okDirect, errDirect := limiter.AllowFor(ctx, key)
	if errDirect == nil {
		t.Fatal("expected AllowFor to return an error when Redis is unreachable")
	}
	if okDirect {
		t.Fatal("expected AllowFor to return false on error")
	}

	// ForKey bound Limiter with failOpen=true (default) allows the request
	if !boundLimiter.Allow(ctx) {
		t.Fatal("expected ForKey().Allow() to fail open and return true on Redis error")
	}

	// If failOpen is set to false, it should deny on error
	limiter.failOpen = false
	if boundLimiter.Allow(ctx) {
		t.Fatal("expected ForKey().Allow() to deny when failOpen is false and Redis errors")
	}
}

func TestRedisLimiter_ConstructorPanics(t *testing.T) {
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	defer func() { _ = rdb.Close() }()

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
				if r := recover(); r == nil {
					t.Fatal("expected NewRedisLimiter to panic on non-positive arguments")
				}
			}()

			_ = NewRedisLimiter(rdb, tt.window, tt.maxRequests)
		})
	}
}
