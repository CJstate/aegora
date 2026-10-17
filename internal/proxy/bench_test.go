package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/syncronix-labs/aegora/internal/cache"
	"github.com/syncronix-labs/aegora/internal/metrics"
	"github.com/syncronix-labs/aegora/internal/middleware"
	"github.com/syncronix-labs/aegora/internal/proxy"
	"github.com/syncronix-labs/aegora/internal/ratelimit"
)

// BenchmarkGatewayChain measures the end-to-end hot path throughput and allocations
// of the representative Aegora middleware chain:
// RequestID -> Metrics -> RateLimit -> Cache -> Gateway (ReverseProxy) -> Upstream Backend.
// It executes a cache-miss GET request that traverses every stage of the pipeline.
func BenchmarkGatewayChain(b *testing.B) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok","backend":"test"}`))
	}))
	defer backend.Close()

	routes := []proxy.RouteConfig{
		{
			Name:        "api-service",
			PathPrefix:  "/api",
			Targets:     []string{backend.URL},
			StripPrefix: "/api",
			LBPolicy:    "round_robin",
		},
	}
	rc := proxy.ReliabilityConfig{
		BreakerFailureThreshold: 5,
		BreakerCooldown:         30 * time.Second,
		BreakerHalfOpenProbes:   2,
		BreakerSuccessThreshold: 2,
		RetryEnabled:            false,
		BaseTransport:           proxy.NewTransport(),
	}
	gw, err := proxy.NewGateway(routes, rc)
	if err != nil {
		b.Fatalf("NewGateway failed: %v", err)
	}

	reg := prometheus.NewRegistry()
	limiter := ratelimit.NewSlidingWindow(time.Minute, 10_000_000)
	rlMiddleware := middleware.RateLimit(func() ratelimit.Limiter {
		return limiter
	}, middleware.IPKey, []string{"/livez", "/readyz"})
	c := cache.NewCache(1000, 64*1024)
	cacheMiddleware := cache.CacheMiddleware(c, time.Minute, 64*1024)

	chain := middleware.New(
		middleware.RequestID,
		metrics.Middleware(reg, gw.Router()),
		rlMiddleware,
		cacheMiddleware,
	).Then(gw)

	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	rec := httptest.NewRecorder()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rec.Body.Reset()
		clear(rec.Header())
		chain.ServeHTTP(rec, req)
	}
}
