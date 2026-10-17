package shedding

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestNewController_Panics(t *testing.T) {
	tests := []struct {
		name          string
		targetLatency time.Duration
		interval      time.Duration
		minRate       float64
		wantPanic     string
	}{
		{
			name:          "zero targetLatency",
			targetLatency: 0,
			interval:      time.Second,
			minRate:       0.05,
			wantPanic:     "shedding: targetLatency must be positive",
		},
		{
			name:          "negative targetLatency",
			targetLatency: -10 * time.Millisecond,
			interval:      time.Second,
			minRate:       0.05,
			wantPanic:     "shedding: targetLatency must be positive",
		},
		{
			name:          "zero interval",
			targetLatency: 50 * time.Millisecond,
			interval:      0,
			minRate:       0.05,
			wantPanic:     "shedding: interval must be positive",
		},
		{
			name:          "negative interval",
			targetLatency: 50 * time.Millisecond,
			interval:      -time.Second,
			minRate:       0.05,
			wantPanic:     "shedding: interval must be positive",
		},
		{
			name:          "zero minRate",
			targetLatency: 50 * time.Millisecond,
			interval:      time.Second,
			minRate:       0.0,
			wantPanic:     "shedding: minRate must be between 0.0 and 1.0 (exclusive)",
		},
		{
			name:          "negative minRate",
			targetLatency: 50 * time.Millisecond,
			interval:      time.Second,
			minRate:       -0.1,
			wantPanic:     "shedding: minRate must be between 0.0 and 1.0 (exclusive)",
		},
		{
			name:          "minRate 1.0",
			targetLatency: 50 * time.Millisecond,
			interval:      time.Second,
			minRate:       1.0,
			wantPanic:     "shedding: minRate must be between 0.0 and 1.0 (exclusive)",
		},
		{
			name:          "minRate greater than 1.0",
			targetLatency: 50 * time.Millisecond,
			interval:      time.Second,
			minRate:       1.5,
			wantPanic:     "shedding: minRate must be between 0.0 and 1.0 (exclusive)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("expected panic %q, got nil", tt.wantPanic)
				}
				msg, ok := r.(string)
				if !ok || msg != tt.wantPanic {
					t.Fatalf("panic message = %v, want %q", r, tt.wantPanic)
				}
			}()

			_ = NewController(tt.targetLatency, tt.interval, tt.minRate, nil, nil)
		})
	}
}

func TestController_InitialAdmitRate(t *testing.T) {
	c := NewController(50*time.Millisecond, time.Second, 0.05, nil, nil)
	if got := c.AdmitRate(); got != 1.0 {
		t.Fatalf("AdmitRate() initially = %v, want 1.0", got)
	}
}

func TestController_PriorityFloor(t *testing.T) {
	c := NewController(50*time.Millisecond, time.Second, 0.05, nil, nil)

	// Set admitRate directly to 0.1
	c.mu.Lock()
	c.admitRate = 0.1
	c.mu.Unlock()

	// High priority: roll 0.5 >= admitRate 0.1, but admitRate >= priorityFloor (0.1), so allowed
	if !c.decide(PriorityHigh, 0.5) {
		t.Fatalf("expected decide(PriorityHigh, 0.5) to be true at admitRate=0.1")
	}

	// Low priority: roll 0.5 >= admitRate 0.1, no priority floor exemption -> shed
	if c.decide(PriorityLow, 0.5) {
		t.Fatalf("expected decide(PriorityLow, 0.5) to be false at admitRate=0.1")
	}

	// Normal priority: roll 0.5 >= admitRate 0.1, no priority floor exemption -> shed
	if c.decide(PriorityNormal, 0.5) {
		t.Fatalf("expected decide(PriorityNormal, 0.5) to be false at admitRate=0.1")
	}

	// At admitRate 0.05 (< priorityFloor 0.1): High priority also dropped if roll >= admitRate
	c.mu.Lock()
	c.admitRate = 0.05
	c.mu.Unlock()

	if c.decide(PriorityHigh, 0.5) {
		t.Fatalf("expected decide(PriorityHigh, 0.5) to be false at admitRate=0.05 (below floor)")
	}
}

func TestController_AIMDMath(t *testing.T) {
	target := 100 * time.Millisecond
	interval := time.Second
	minRate := 0.05

	c := NewController(target, interval, minRate, nil, nil)

	// Helper to force an adjustment by resetting lastAdjust
	forceAdjust := func(latency time.Duration) {
		c.mu.Lock()
		c.lastAdjust = time.Now().Add(-2 * interval)
		c.mu.Unlock()
		c.maybeAdjust(latency)
	}

	// 1. Sustained above-target latencies decrease admitRate multiplicatively (* 0.9)
	expectedRate := 1.0
	for step := 1; step <= 10; step++ {
		forceAdjust(150 * time.Millisecond) // above target 100ms
		expectedRate *= 0.9
		if math.Abs(c.AdmitRate()-expectedRate) > 1e-9 {
			t.Fatalf("step %d: AdmitRate() = %v, want %v", step, c.AdmitRate(), expectedRate)
		}
	}

	// 2. Drive rate further down until it hits minRate floor
	for i := 0; i < 50; i++ {
		forceAdjust(200 * time.Millisecond)
	}
	if got := c.AdmitRate(); got != minRate {
		t.Fatalf("AdmitRate() = %v after heavy shed, want minRate %v", got, minRate)
	}

	// 3. Sustained below-target latencies increase additively (+0.05) up to 1.0
	currRate := minRate
	for step := 1; step <= 25; step++ {
		forceAdjust(50 * time.Millisecond) // below target 100ms
		currRate = math.Min(1.0, currRate+0.05)
		if math.Abs(c.AdmitRate()-currRate) > 1e-9 {
			t.Fatalf("step %d: AdmitRate() = %v, want %v", step, c.AdmitRate(), currRate)
		}
	}

	if got := c.AdmitRate(); got != 1.0 {
		t.Fatalf("AdmitRate() capped = %v, want 1.0", got)
	}

	// 4. Further below-target adjustments do not exceed 1.0
	forceAdjust(10 * time.Millisecond)
	if got := c.AdmitRate(); got != 1.0 {
		t.Fatalf("AdmitRate() after extra increase = %v, want 1.0", got)
	}
}

func TestController_AdjustmentIntervalThrottling(t *testing.T) {
	c := NewController(100*time.Millisecond, time.Hour, 0.05, nil, nil)

	// Call maybeAdjust immediately without manipulating lastAdjust
	c.maybeAdjust(200 * time.Millisecond)
	if got := c.AdmitRate(); got != 1.0 {
		t.Fatalf("AdmitRate() changed prematurely without interval elapsing: got %v, want 1.0", got)
	}
}

func TestController_P99Percentile(t *testing.T) {
	c := NewController(100*time.Millisecond, time.Second, 0.05, nil, nil)

	// Empty ring buffer returns 0
	if p99 := c.p99Locked(); p99 != 0 {
		t.Fatalf("p99Locked() on empty buffer = %v, want 0", p99)
	}

	// Insert 100 values: 1ms, 2ms, ..., 100ms
	for i := 1; i <= 100; i++ {
		c.recordLatency(time.Duration(i) * time.Millisecond)
	}

	c.mu.Lock()
	p99 := c.p99Locked()
	c.mu.Unlock()

	// 99th percentile of 100 values [1..100] is 99ms (index 98 in 0-indexed)
	if p99 != 99*time.Millisecond {
		t.Fatalf("p99 = %v, want 99ms", p99)
	}
}

func TestController_Signals(t *testing.T) {
	c := NewController(100*time.Millisecond, time.Second, 0.05, nil, nil)
	c.recordLatency(42 * time.Millisecond)
	c.inflight.Store(5)

	sig := c.Signals()
	if sig.InFlight != 5 {
		t.Fatalf("Signals.InFlight = %d, want 5", sig.InFlight)
	}
	if sig.P99Latency != 42*time.Millisecond {
		t.Fatalf("Signals.P99Latency = %v, want 42ms", sig.P99Latency)
	}
}

func TestController_AdmitMiddleware_AllowAndShed(t *testing.T) {
	c := NewController(50*time.Millisecond, time.Second, 0.05, []string{"/high"}, []string{"/low"})

	handler := c.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	// Case 1: AdmitRate = 1.0 -> All requests admitted
	req := httptest.NewRequest(http.MethodGet, "/low", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want 'ok'", rec.Body.String())
	}

	// Verify sample was recorded into ring buffer
	c.mu.Lock()
	fill := c.fillCount
	c.mu.Unlock()
	if fill != 1 {
		t.Fatalf("fillCount = %d, want 1", fill)
	}

	// Case 2: Force shed (admitRate = 0.05, low priority)
	c.mu.Lock()
	c.admitRate = 0.05
	c.priorityFloor = 0.5 // raise floor above 0.05 so everything checks roll
	c.mu.Unlock()

	// High rate of drops when roll < 0.05
	shedReq := httptest.NewRequest(http.MethodGet, "/low", nil)
	// Mock or check that when decide returns false, 503 is returned
	// Since decide uses rand.Float64(), let's set admitRate to 0.000000001 or test deterministic handler
	c.mu.Lock()
	c.admitRate = 0.0000000001
	c.priorityFloor = 0.9
	c.mu.Unlock()

	shedRec := httptest.NewRecorder()
	handler.ServeHTTP(shedRec, shedReq)

	if shedRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (503)", shedRec.Code, http.StatusServiceUnavailable)
	}
	if got := shedRec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want '1'", got)
	}
	if got := shedRec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want 'application/json'", got)
	}

	var errResp map[string]string
	if err := json.Unmarshal(shedRec.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("failed to decode shed response JSON: %v", err)
	}
	if errResp["error"] != "server overloaded" {
		t.Fatalf("error JSON = %v, want 'server overloaded'", errResp["error"])
	}

	// Shed requests should NOT record latency
	c.mu.Lock()
	fillAfterShed := c.fillCount
	c.mu.Unlock()
	if fillAfterShed != 1 {
		t.Fatalf("fillCount changed after shed: got %d, want 1", fillAfterShed)
	}
}

func TestController_ConcurrencyRace(t *testing.T) {
	t.Parallel()

	c := NewController(50*time.Millisecond, 10*time.Millisecond, 0.05, []string{"/payments"}, []string{"/analytics"})

	handler := c.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const workers = 16
	const reqsPerWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)

	paths := []string{"/payments", "/analytics", "/general"}

	for i := 0; i < workers; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < reqsPerWorker; j++ {
				path := paths[(workerID+j)%len(paths)]
				req := httptest.NewRequest(http.MethodGet, path, nil)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)

				// Interleave reads of AdmitRate and Signals
				_ = c.AdmitRate()
				_ = c.Signals()
			}
		}(i)
	}

	wg.Wait()
}
