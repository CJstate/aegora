package metrics

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/syncronix-labs/aegora/internal/proxy"
)

type stubRouter struct {
	matchFn func(r *http.Request) *proxy.RouteConfig
}

func (s *stubRouter) Match(r *http.Request) *proxy.RouteConfig {
	if s.matchFn != nil {
		return s.matchFn(r)
	}
	return nil
}

func TestMetrics_Middleware_HitAndMissWithLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	router := &stubRouter{
		matchFn: func(r *http.Request) *proxy.RouteConfig {
			if r.URL.Path == "/api/users" {
				return &proxy.RouteConfig{Name: "users-service"}
			}
			return nil
		},
	}

	var inflightDuringHandler float64
	var mu sync.Mutex

	handler := m.Middleware(router)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inflightDuringHandler = testutil.ToFloat64(m.Inflight.WithLabelValues("users-service"))
		mu.Unlock()

		if r.URL.Path == "/api/users" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))

	// 1. First request: /api/users returns 200, matching "users-service"
	req1 := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec1.Code)
	}

	mu.Lock()
	if inflightDuringHandler != 1.0 {
		t.Errorf("expected inflight during execution to be 1.0, got %f", inflightDuringHandler)
	}
	mu.Unlock()

	// Inflight gauge should return to 0 after request completion
	if inflightAfter := testutil.ToFloat64(m.Inflight.WithLabelValues("users-service")); inflightAfter != 0.0 {
		t.Errorf("expected inflight after request to be 0.0, got %f", inflightAfter)
	}

	// Counter for users-service GET 200 should be 1.0
	cnt200 := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("users-service", http.MethodGet, "200"))
	if cnt200 != 1.0 {
		t.Errorf("expected aegora_requests_total for users-service GET 200 to be 1.0, got %f", cnt200)
	}

	// 2. Second request: /api/unknown returns 404, yields route="unmatched"
	req2 := httptest.NewRequest(http.MethodPost, "/api/unknown", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec2.Code)
	}

	// Inflight for unmatched should return to 0
	if inflightUnmatched := testutil.ToFloat64(m.Inflight.WithLabelValues("unmatched")); inflightUnmatched != 0.0 {
		t.Errorf("expected inflight for unmatched to be 0.0, got %f", inflightUnmatched)
	}

	// Counter for unmatched POST 404 should be 1.0
	cnt404 := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("unmatched", http.MethodPost, "404"))
	if cnt404 != 1.0 {
		t.Errorf("expected aegora_requests_total for unmatched POST 404 to be 1.0, got %f", cnt404)
	}

	// 3. Verify histogram observed both requests
	metricFamilies, err := reg.Gather()
	if err != nil {
		t.Fatalf("reg.Gather() failed: %v", err)
	}

	var histTotalCount uint64
	for _, mf := range metricFamilies {
		if mf.GetName() == "aegora_request_duration_seconds" {
			for _, metric := range mf.GetMetric() {
				if hist := metric.GetHistogram(); hist != nil {
					histTotalCount += hist.GetSampleCount()
				}
			}
		}
	}

	if histTotalCount != 2 {
		t.Errorf("expected histogram to have observed 2 total samples across series, got %d", histTotalCount)
	}
}

func TestMetrics_Middleware_Implicit200OnWriteWithoutWriteHeader(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	handler := m.Middleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Does not call WriteHeader; only calls Write directly
		_, _ = w.Write([]byte("implicit ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/implicit", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	cnt := testutil.ToFloat64(m.RequestsTotal.WithLabelValues("unmatched", http.MethodGet, "200"))
	if cnt != 1.0 {
		t.Errorf("expected aegora_requests_total with implicit 200 code to be 1.0, got %f", cnt)
	}
}

func TestMetrics_NewPanicsOnNilRegistry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected New to panic on nil registry")
		}
	}()
	_ = New(nil)
}
