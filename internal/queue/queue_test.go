package queue

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPriority_String(t *testing.T) {
	tests := []struct {
		p    Priority
		want string
	}{
		{PriorityHigh, "high"},
		{PriorityNormal, "normal"},
		{PriorityLow, "low"},
		{Priority(99), "Priority(99)"},
	}

	for _, tt := range tests {
		if got := tt.p.String(); got != tt.want {
			t.Errorf("Priority(%d).String() = %q, want %q", tt.p, got, tt.want)
		}
	}
}

func TestClassify_TableDriven(t *testing.T) {
	highPaths := []string{"/api/payments", "/checkout"}
	lowPaths := []string{"/api/analytics", "/telemetry"}

	tests := []struct {
		name      string
		path      string
		headerVal string
		wantPrior Priority
	}{
		// 1. Header wins over path rules
		{
			name:      "X-Priority high overrides low path",
			path:      "/api/analytics/events",
			headerVal: "high",
			wantPrior: PriorityHigh,
		},
		{
			name:      "X-Priority High case-insensitive",
			path:      "/api/other",
			headerVal: "HIGH",
			wantPrior: PriorityHigh,
		},
		{
			name:      "X-Priority low overrides high path",
			path:      "/api/payments/charge",
			headerVal: "low",
			wantPrior: PriorityLow,
		},
		{
			name:      "X-Priority LOW case-insensitive",
			path:      "/checkout",
			headerVal: "LOW",
			wantPrior: PriorityLow,
		},
		{
			name:      "X-Priority normal overrides high path",
			path:      "/api/payments",
			headerVal: "normal",
			wantPrior: PriorityNormal,
		},
		// 2. Path prefix matching when header is absent
		{
			name:      "high path prefix match",
			path:      "/api/payments/pay",
			headerVal: "",
			wantPrior: PriorityHigh,
		},
		{
			name:      "second high path prefix match",
			path:      "/checkout/cart",
			headerVal: "",
			wantPrior: PriorityHigh,
		},
		{
			name:      "low path prefix match",
			path:      "/api/analytics/metrics",
			headerVal: "",
			wantPrior: PriorityLow,
		},
		{
			name:      "second low path prefix match",
			path:      "/telemetry/stats",
			headerVal: "",
			wantPrior: PriorityLow,
		},
		// 3. Fallback to default PriorityNormal
		{
			name:      "standard route defaults to normal",
			path:      "/api/users",
			headerVal: "",
			wantPrior: PriorityNormal,
		},
		{
			name:      "unknown header value falls back to normal",
			path:      "/api/users",
			headerVal: "invalid-tier",
			wantPrior: PriorityNormal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.headerVal != "" {
				req.Header.Set("X-Priority", tt.headerVal)
			}

			got := Classify(req, highPaths, lowPaths)
			if got != tt.wantPrior {
				t.Errorf("Classify(%q, %q) = %v, want %v", tt.path, tt.headerVal, got, tt.wantPrior)
			}
		})
	}

	t.Run("nil request defaults to normal", func(t *testing.T) {
		if got := Classify(nil, highPaths, lowPaths); got != PriorityNormal {
			t.Errorf("Classify(nil) = %v, want %v", got, PriorityNormal)
		}
	})
}

func TestQueue_FullQueueImmediate503(t *testing.T) {
	cd := NewCodel(100*time.Millisecond, time.Second)
	// maxDepth = 2 per priority, maxInflight = 1
	q := NewQueue(1, 2, cd, nil, nil)
	q.running = true // mark running without starting background dispatcher, so tickets stay queued

	handler := q.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Fill normal queue to maxDepth (2)
	q.mu.Lock()
	q.normalQueue = append(q.normalQueue, &ticket{priority: PriorityNormal, done: make(chan ticketResult, 1)})
	q.normalQueue = append(q.normalQueue, &ticket{priority: PriorityNormal, done: make(chan ticketResult, 1)})
	q.mu.Unlock()

	// 3rd request exceeding maxDepth must return 503 immediately
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	start := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(start)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for full queue, got %d", rec.Code)
	}

	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected immediate 503 response, took %v", elapsed)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if body["error"] != "server overloaded" {
		t.Fatalf("expected error 'server overloaded', got %q", body["error"])
	}
}

func TestQueue_AdmittedFlowEndToEnd(t *testing.T) {
	cd := NewCodel(500*time.Millisecond, time.Second)
	q := NewQueue(1, 10, cd, nil, nil)
	q.Start()
	defer q.Stop()

	unblockReq1 := make(chan struct{})
	req1Started := make(chan struct{})
	req2Started := make(chan struct{})
	var req2Active atomic.Bool

	handler := q.Admit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-Index") == "1" {
			close(req1Started)
			<-unblockReq1
		} else {
			req2Active.Store(true)
			close(req2Started)
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Request 1 blocks inside handler
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rec1 := httptest.NewRecorder()
		req1 := httptest.NewRequest(http.MethodGet, "/req1", nil)
		req1.Header.Set("X-Request-Index", "1")
		handler.ServeHTTP(rec1, req1)
		if rec1.Code != http.StatusOK {
			t.Errorf("req1 failed with code %d", rec1.Code)
		}
	}()

	<-req1Started

	// Launch Request 2; with maxInflight = 1, it must wait for Request 1 to finish
	go func() {
		defer wg.Done()
		rec2 := httptest.NewRecorder()
		req2 := httptest.NewRequest(http.MethodGet, "/req2", nil)
		req2.Header.Set("X-Request-Index", "2")
		handler.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Errorf("req2 failed with code %d", rec2.Code)
		}
	}()

	// Verify Request 2 is queued and has NOT started while Request 1 holds the single inflight slot
	time.Sleep(30 * time.Millisecond)
	if req2Active.Load() {
		t.Fatal("expected request 2 to wait for request 1 while maxInflight=1")
	}

	// Unblock Request 1
	close(unblockReq1)

	// Wait for Request 2 to start and both to finish
	select {
	case <-req2Started:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for request 2 to start after request 1 completed")
	}

	wg.Wait()
}

func TestQueue_DequeuePriorityOrdering_Pop(t *testing.T) {
	cd := NewCodel(100*time.Millisecond, time.Second)
	q := NewQueue(5, 50, cd, nil, nil)
	// Do not start dispatcher; test pop directly

	tLow := &ticket{priority: PriorityLow, done: make(chan ticketResult, 1)}
	tNormal := &ticket{priority: PriorityNormal, done: make(chan ticketResult, 1)}
	tHigh := &ticket{priority: PriorityHigh, done: make(chan ticketResult, 1)}

	q.mu.Lock()
	q.lowQueue = append(q.lowQueue, tLow)
	q.normalQueue = append(q.normalQueue, tNormal)
	q.highQueue = append(q.highQueue, tHigh)
	q.mu.Unlock()

	// High priority must be popped first
	got1 := q.pop()
	if got1 != tHigh {
		t.Fatalf("expected pop 1 to be high priority ticket, got %v", got1)
	}

	// Normal priority must be popped second
	got2 := q.pop()
	if got2 != tNormal {
		t.Fatalf("expected pop 2 to be normal priority ticket, got %v", got2)
	}

	// Low priority must be popped third
	got3 := q.pop()
	if got3 != tLow {
		t.Fatalf("expected pop 3 to be low priority ticket, got %v", got3)
	}

	// Queue is now empty
	if got4 := q.pop(); got4 != nil {
		t.Fatalf("expected nil from empty queue, got %v", got4)
	}
}

func TestQueue_StarvationGuard(t *testing.T) {
	cd := NewCodel(100*time.Millisecond, time.Second)
	q := NewQueue(5, 50, cd, nil, nil)

	// Enqueue 12 High tickets and 2 Low tickets
	q.mu.Lock()
	for i := 0; i < 12; i++ {
		q.highQueue = append(q.highQueue, &ticket{priority: PriorityHigh, done: make(chan ticketResult, 1)})
	}
	tLow1 := &ticket{priority: PriorityLow, done: make(chan ticketResult, 1)}
	tLow2 := &ticket{priority: PriorityLow, done: make(chan ticketResult, 1)}
	q.lowQueue = append(q.lowQueue, tLow1, tLow2)
	q.mu.Unlock()

	// First 10 pops must be High priority
	for i := 1; i <= 10; i++ {
		popped := q.pop()
		if popped.priority != PriorityHigh {
			t.Fatalf("pop %d: expected High priority, got %v", i, popped.priority)
		}
	}

	// 11th pop must be Low priority due to the starvation guard (at least once per 10 high serves)
	popped11 := q.pop()
	if popped11 != tLow1 {
		t.Fatalf("pop 11: expected Low priority ticket due to starvation guard, got priority %v", popped11.priority)
	}

	// 12th pop returns back to High priority
	popped12 := q.pop()
	if popped12.priority != PriorityHigh {
		t.Fatalf("pop 12: expected High priority ticket after starvation service, got %v", popped12.priority)
	}
}

func TestQueue_StopUnblocksWaiters(t *testing.T) {
	cd := NewCodel(100*time.Millisecond, time.Second)
	q := NewQueue(1, 10, cd, nil, nil)
	q.Start()

	// Block the single inflight slot with request 1
	unblock1 := make(chan struct{})
	started1 := make(chan struct{})

	handler := q.Admit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Req") == "1" {
			close(started1)
			<-unblock1
		}
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Req", "1")
		handler.ServeHTTP(rec, req)
	}()

	<-started1

	// Launch requests 2 and 3 into the queue
	rec2 := httptest.NewRecorder()
	rec3 := httptest.NewRecorder()

	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/2", nil)
		handler.ServeHTTP(rec2, req)
	}()

	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/3", nil)
		handler.ServeHTTP(rec3, req)
	}()

	// Wait briefly so requests 2 and 3 are enqueued
	time.Sleep(30 * time.Millisecond)

	// Stop the queue: should immediately drop all queued waiters
	q.Stop()

	// Clean up request 1
	close(unblock1)

	wg.Wait()

	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("request 2 expected 503 on Stop(), got %d", rec2.Code)
	}
	if rec3.Code != http.StatusServiceUnavailable {
		t.Errorf("request 3 expected 503 on Stop(), got %d", rec3.Code)
	}
}

func TestQueue_ClientCancelRemovesTicket(t *testing.T) {
	cd := NewCodel(100*time.Millisecond, time.Second)
	q := NewQueue(1, 10, cd, nil, nil)
	// Do not start dispatcher, so ticket stays in queue
	q.running = true

	ctx, cancel := context.WithCancel(context.Background())

	handler := q.Admit(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/cancel", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(rec, req)
	}()

	// Cancel context while waiting in queue
	time.Sleep(10 * time.Millisecond)
	cancel()
	wg.Wait()

	// Ticket should have been removed from the queue
	q.mu.Lock()
	count := len(q.normalQueue)
	q.mu.Unlock()

	if count != 0 {
		t.Fatalf("expected queue to be empty after client cancellation, got %d tickets", count)
	}
}
