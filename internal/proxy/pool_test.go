package proxy

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPool_NewPoolValidation(t *testing.T) {
	tests := []struct {
		name    string
		targets []string
		policy  string
		wantErr bool
		errText string
	}{
		{
			name:    "empty targets returns error",
			targets: []string{},
			policy:  "round_robin",
			wantErr: true,
			errText: "must have at least one target",
		},
		{
			name:    "invalid target url returns error naming target",
			targets: []string{"http://127.0.0.1:8080", "::bad-url::"},
			policy:  "round_robin",
			wantErr: true,
			errText: "::bad-url::",
		},
		{
			name:    "missing scheme returns error naming target",
			targets: []string{"127.0.0.1:8080"},
			policy:  "round_robin",
			wantErr: true,
			errText: "127.0.0.1:8080",
		},
		{
			name:    "invalid policy returns error",
			targets: []string{"http://127.0.0.1:8080"},
			policy:  "random_walk",
			wantErr: true,
			errText: "unsupported load balancing policy",
		},
		{
			name:    "default policy becomes round_robin",
			targets: []string{"http://127.0.0.1:8080"},
			policy:  "",
			wantErr: false,
		},
		{
			name:    "least_connections policy accepted",
			targets: []string{"http://127.0.0.1:8080"},
			policy:  "least_connections",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPool(tt.targets, tt.policy)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NewPool() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if tt.errText != "" && !strings.Contains(err.Error(), tt.errText) {
					t.Fatalf("error %q does not contain expected substring %q", err.Error(), tt.errText)
				}
				return
			}
			if p == nil {
				t.Fatal("expected non-nil pool on success")
			}
			if tt.policy == "" && p.policy != "round_robin" {
				t.Fatalf("expected default policy 'round_robin', got %q", p.policy)
			}
			if p.cooldown != 30*time.Second {
				t.Fatalf("expected default cooldown 30s, got %v", p.cooldown)
			}
		})
	}
}

func TestPool_RoundRobin(t *testing.T) {
	targets := []string{
		"http://10.0.0.1:8080",
		"http://10.0.0.2:8080",
		"http://10.0.0.3:8080",
	}

	pool, err := NewPool(targets, "round_robin")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Make 6 consecutive calls and assert alternating round-robin behavior
	wantOrder := []int{0, 1, 2, 0, 1, 2}
	for i, wantIdx := range wantOrder {
		u, idx, ok := pool.Next()
		if !ok {
			t.Fatalf("call %d: expected ok=true", i)
		}
		if idx != wantIdx {
			t.Fatalf("call %d: expected index %d, got %d", i, wantIdx, idx)
		}
		if u.String() != targets[wantIdx] {
			t.Fatalf("call %d: expected URL %s, got %s", i, targets[wantIdx], u.String())
		}
		// Balance in-flight count
		pool.Done(idx)
	}
}

func TestPool_LeastConnections(t *testing.T) {
	targets := []string{
		"http://10.0.0.1:8080",
		"http://10.0.0.2:8080",
	}

	pool, err := NewPool(targets, "least_connections")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// 1. Initial selection selects index 0, incrementing inFlight[0] to 1
	_, idx1, ok := pool.Next()
	if !ok || idx1 != 0 {
		t.Fatalf("expected index 0 on first call, got idx=%d ok=%v", idx1, ok)
	}

	// 2. Second selection must prefer index 1 since inFlight[0]=1 and inFlight[1]=0
	_, idx2, ok := pool.Next()
	if !ok || idx2 != 1 {
		t.Fatalf("expected index 1 on second call, got idx=%d ok=%v", idx2, ok)
	}

	// 3. Mark index 0 as done; inFlight[0] is now 0, while inFlight[1] is 1
	pool.Done(idx1)

	// 4. Third selection must prefer index 0 because it now has fewer in-flight requests (0 vs 1)
	_, idx3, ok := pool.Next()
	if !ok || idx3 != 0 {
		t.Fatalf("expected index 0 on third call, got idx=%d ok=%v", idx3, ok)
	}

	// Cleanup remaining in-flights
	pool.Done(idx2)
	pool.Done(idx3)
}

func TestPool_ReportFailureAndSuccess(t *testing.T) {
	targets := []string{
		"http://10.0.0.1:8080",
		"http://10.0.0.2:8080",
	}

	pool, err := NewPool(targets, "round_robin")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Report failure on target 0
	pool.ReportFailure(0)

	// Next calls should skip target 0 and only return target 1
	for i := 0; i < 5; i++ {
		_, idx, ok := pool.Next()
		if !ok {
			t.Fatalf("call %d: expected ok=true, got false", i)
		}
		if idx != 1 {
			t.Fatalf("call %d: expected index 1 (skipping unhealthy 0), got %d", i, idx)
		}
		pool.Done(idx)
	}

	// Report success on target 0 clears the unhealthy status
	pool.ReportSuccess(0)

	// Next calls should now alternate across both targets again
	sawZero := false
	for i := 0; i < 4; i++ {
		_, idx, ok := pool.Next()
		if !ok {
			t.Fatalf("call %d: expected ok=true", i)
		}
		if idx == 0 {
			sawZero = true
		}
		pool.Done(idx)
	}
	if !sawZero {
		t.Fatal("expected target 0 to be returned again after ReportSuccess")
	}
}

func TestPool_CooldownExpiration(t *testing.T) {
	targets := []string{
		"http://10.0.0.1:8080",
		"http://10.0.0.2:8080",
	}

	pool, err := NewPool(targets, "round_robin")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Shorten cooldown for testing
	pool.cooldown = 30 * time.Millisecond

	// Mark target 0 as failed
	pool.ReportFailure(0)

	// Immediately, target 0 must be skipped
	_, idx, ok := pool.Next()
	if !ok || idx != 1 {
		t.Fatalf("expected index 1, got idx=%d ok=%v", idx, ok)
	}
	pool.Done(idx)

	// Wait for cooldown to expire
	time.Sleep(45 * time.Millisecond)

	// Target 0 should now be healthy again
	sawZero := false
	for i := 0; i < 4; i++ {
		_, idx, ok := pool.Next()
		if !ok {
			t.Fatalf("expected ok=true")
		}
		if idx == 0 {
			sawZero = true
		}
		pool.Done(idx)
	}
	if !sawZero {
		t.Fatal("expected target 0 to recover after cooldown expired")
	}
}

func TestPool_AllDown(t *testing.T) {
	targets := []string{
		"http://10.0.0.1:8080",
		"http://10.0.0.2:8080",
	}

	pool, err := NewPool(targets, "round_robin")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Mark both targets as failed
	pool.ReportFailure(0)
	pool.ReportFailure(1)

	// Next should return ok=false
	target, idx, ok := pool.Next()
	if ok {
		t.Fatalf("expected ok=false when all targets are unhealthy, got ok=true (idx=%d, target=%v)", idx, target)
	}
	if target != nil {
		t.Fatalf("expected nil target, got %v", target)
	}
}

func TestPool_ConcurrentNextDoneRace(t *testing.T) {
	for _, policy := range []string{"round_robin", "least_connections"} {
		t.Run(policy, func(t *testing.T) {
			targets := []string{
				"http://10.0.0.1:8080",
				"http://10.0.0.2:8080",
				"http://10.0.0.3:8080",
				"http://10.0.0.4:8080",
			}

			pool, err := NewPool(targets, policy)
			if err != nil {
				t.Fatalf("NewPool: %v", err)
			}
			pool.cooldown = 10 * time.Millisecond

			const numWorkers = 20
			const iterations = 200

			var wg sync.WaitGroup
			wg.Add(numWorkers)

			for w := 0; w < numWorkers; w++ {
				go func(workerID int) {
					defer wg.Done()
					for i := 0; i < iterations; i++ {
						_, idx, ok := pool.Next()
						if ok {
							// Simulate brief in-flight work
							time.Sleep(10 * time.Microsecond)
							pool.Done(idx)
						}

						// Periodically report failure or success to test concurrency under health changes
						switch i % 30 {
						case 0:
							targetIdx := (workerID + i) % len(targets)
							pool.ReportFailure(targetIdx)
						case 15:
							targetIdx := (workerID + i) % len(targets)
							pool.ReportSuccess(targetIdx)
						}
					}
				}(w)
			}

			wg.Wait()

			// Ensure pool is still operational after concurrent barrage
			for i := 0; i < len(targets); i++ {
				pool.ReportSuccess(i)
			}
			_, _, ok := pool.Next()
			if !ok {
				t.Fatal("expected pool to be functional after concurrent test")
			}
		})
	}
}

func TestPool_DoneOutOfBounds(t *testing.T) {
	pool, err := NewPool([]string{"http://10.0.0.1:8080"}, "round_robin")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}

	// Should not panic on out-of-bounds indices
	pool.Done(-1)
	pool.Done(100)
	pool.ReportFailure(-1)
	pool.ReportFailure(100)
	pool.ReportSuccess(-1)
	pool.ReportSuccess(100)
}
