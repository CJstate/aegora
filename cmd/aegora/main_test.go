package main

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/syncronix-labs/aegora/internal/config"
)

func newTestConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "test-route",
				PathPrefix: "/api",
				Targets:    []string{"http://127.0.0.1:8081"},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "info",
			MetricsAddr: "127.0.0.1:9090",
		},
	}
}

func TestBuildHandler_EndpointsAndMiddleware(t *testing.T) {
	cfg := newTestConfig()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	tests := []struct {
		name         string
		method       string
		path         string
		wantStatus   int
		wantBody     string
		checkJSONMsg bool
	}{
		{
			name:       "GET /livez returns 200 ok",
			method:     http.MethodGet,
			path:       "/livez",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
		{
			name:       "GET /readyz returns 200 ok",
			method:     http.MethodGet,
			path:       "/readyz",
			wantStatus: http.StatusOK,
			wantBody:   `{"status":"ok"}`,
		},
		{
			name:       "POST /livez returns 405 Method Not Allowed",
			method:     http.MethodPost,
			path:       "/livez",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "POST /readyz returns 405 Method Not Allowed",
			method:     http.MethodPost,
			path:       "/readyz",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:       "GET /unknown returns 404 Not Found",
			method:     http.MethodGet,
			path:       "/unknown",
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error":"no route matched"}`,
		},
		{
			name:       "POST /other returns 404 Not Found",
			method:     http.MethodPost,
			path:       "/other",
			wantStatus: http.StatusNotFound,
			wantBody:   `{"error":"no route matched"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, nil)

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status mismatch: got %d, want %d", rec.Code, tt.wantStatus)
			}

			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Fatalf("body mismatch: got %q, want %q", rec.Body.String(), tt.wantBody)
			}

			// Verify that every response carries an X-Request-ID header
			reqID := rec.Header().Get("X-Request-ID")
			if reqID == "" {
				t.Fatal("expected X-Request-ID header on response, got none")
			}
			if len(reqID) != 32 {
				t.Fatalf("expected 32-character hex X-Request-ID, got %q", reqID)
			}
		})
	}
}

func TestBuildHandler_PropagatesExistingRequestID(t *testing.T) {
	cfg := newTestConfig()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	const incomingID = "custom-client-id-abc"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	req.Header.Set("X-Request-ID", incomingID)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if got := rec.Header().Get("X-Request-ID"); got != incomingID {
		t.Fatalf("expected echoed X-Request-ID %q, got %q", incomingID, got)
	}
}

func TestBuildHandler_MultiBackendRoutingAndStripPrefix(t *testing.T) {
	var backend1Path, backend2Path string
	var mu sync.Mutex

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backend1Path = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"backend":"1"}`))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		backend2Path = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"backend":"2"}`))
	}))
	defer backend2.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:        "users-route",
				PathPrefix:  "/users",
				Targets:     []string{backend1.URL},
				StripPrefix: "/users",
			},
			{
				Name:        "orders-route",
				PathPrefix:  "/orders",
				Targets:     []string{backend2.URL},
				StripPrefix: "/orders",
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "info",
			MetricsAddr: "127.0.0.1:9090",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	// 1. Request to /users/profile lands on backend 1 and strip prefix yields /profile
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/users/profile", nil)
	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}
	if rec1.Body.String() != `{"backend":"1"}` {
		t.Fatalf("expected backend 1 response, got %s", rec1.Body.String())
	}

	mu.Lock()
	gotPath1 := backend1Path
	mu.Unlock()
	if gotPath1 != "/profile" {
		t.Fatalf("expected stripped path /profile on backend 1, got %q", gotPath1)
	}

	// 2. Request to /orders/42 lands on backend 2 and strip prefix yields /42
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/orders/42", nil)
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rec2.Code, rec2.Body.String())
	}
	if rec2.Body.String() != `{"backend":"2"}` {
		t.Fatalf("expected backend 2 response, got %s", rec2.Body.String())
	}

	mu.Lock()
	gotPath2 := backend2Path
	mu.Unlock()
	if gotPath2 != "/42" {
		t.Fatalf("expected stripped path /42 on backend 2, got %q", gotPath2)
	}
}

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input   string
		want    slog.Level
		wantErr bool
	}{
		{input: "debug", want: slog.LevelDebug, wantErr: false},
		{input: "DEBUG", want: slog.LevelDebug, wantErr: false},
		{input: "info", want: slog.LevelInfo, wantErr: false},
		{input: "Info", want: slog.LevelInfo, wantErr: false},
		{input: "warn", want: slog.LevelWarn, wantErr: false},
		{input: "WARN", want: slog.LevelWarn, wantErr: false},
		{input: "error", want: slog.LevelError, wantErr: false},
		{input: "ERROR", want: slog.LevelError, wantErr: false},
		{input: "invalid", wantErr: true},
		{input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseLogLevel(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseLogLevel(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("parseLogLevel(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildHandler_AccessLogging(t *testing.T) {
	cfg := newTestConfig()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)

	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if logOutput == "" {
		t.Fatal("expected access log output, got empty")
	}
}

func TestBuildHandler_RateLimiting(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"proxied"}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "api-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		RateLimit: config.RateLimitConfig{
			Enabled:     true,
			Limiter:     "sliding_window",
			MaxRequests: 2,
			Window:      config.Duration(time.Minute),
			Key:         "ip",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	// 1st request -> 200 OK
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req1.RemoteAddr = "192.168.1.50:1234"
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("request 1 expected 200, got %d", rec1.Code)
	}

	// 2nd request -> 200 OK
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req2.RemoteAddr = "192.168.1.50:1234"
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("request 2 expected 200, got %d", rec2.Code)
	}

	// 3rd (N+1th) rapid request -> 429 Too Many Requests
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	req3.RemoteAddr = "192.168.1.50:1234"
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusTooManyRequests {
		t.Fatalf("N+1th request expected 429, got %d", rec3.Code)
	}

	if limit := rec3.Header().Get("X-RateLimit-Limit"); limit != "2" {
		t.Fatalf("expected X-RateLimit-Limit '2', got %q", limit)
	}
	if rem := rec3.Header().Get("X-RateLimit-Remaining"); rem != "0" {
		t.Fatalf("expected X-RateLimit-Remaining '0', got %q", rem)
	}
	if retryAfter := rec3.Header().Get("Retry-After"); retryAfter == "" {
		t.Fatalf("expected Retry-After header, got empty")
	}
	if body := rec3.Body.String(); body != `{"error":"rate limit exceeded"}` {
		t.Fatalf("expected body %q, got %q", `{"error":"rate limit exceeded"}`, body)
	}

	// Verify that /livez is never limited, even when client is rate-limited
	for i := 0; i < 5; i++ {
		recLivez := httptest.NewRecorder()
		reqLivez := httptest.NewRequest(http.MethodGet, "/livez", nil)
		reqLivez.RemoteAddr = "192.168.1.50:1234"
		handler.ServeHTTP(recLivez, reqLivez)
		if recLivez.Code != http.StatusOK {
			t.Fatalf("/livez call %d expected 200, got %d", i+1, recLivez.Code)
		}
	}
}

func TestBuildHandler_QueueAdmission(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"proxied"}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "api-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		Queue: config.QueueConfig{
			Enabled:       true,
			MaxInflight:   2,
			MaxQueueDepth: 5,
			CodelTarget:   config.Duration(100 * time.Millisecond),
			CodelInterval: config.Duration(time.Second),
			PriorityPaths: config.PriorityPathsConfig{
				High: []string{"/api/priority"},
				Low:  []string{"/api/bulk"},
			},
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, q, err := buildApp(cfg, logger)
	if err != nil {
		t.Fatalf("buildApp: %v", err)
	}
	if q == nil {
		t.Fatal("expected non-nil queue when queue is enabled")
	}
	q.Start()
	defer q.Stop()

	// 1. Livez bypasses queue completely
	recLivez := httptest.NewRecorder()
	reqLivez := httptest.NewRequest(http.MethodGet, "/livez", nil)
	handler.ServeHTTP(recLivez, reqLivez)
	if recLivez.Code != http.StatusOK {
		t.Fatalf("expected 200 for /livez, got %d", recLivez.Code)
	}

	// 2. Normal request through queue to backend succeeds
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/data, got %d (body: %s)", rec1.Code, rec1.Body.String())
	}

	// 3. High priority request through queue to backend succeeds
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/priority/pay", nil)
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for /api/priority/pay, got %d", rec2.Code)
	}
}

func TestBuildHandler_Shedding(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"proxied"}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "api-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		Shedding: config.SheddingConfig{
			Enabled:        true,
			TargetP99:      config.Duration(500 * time.Millisecond),
			MinAdmitRate:   0.05,
			AdjustInterval: config.Duration(time.Second),
			PriorityPaths: config.PriorityPathsConfig{
				High: []string{"/api/payments"},
				Low:  []string{"/api/analytics"},
			},
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	// 1. Initial requests pass through since initial admitRate is 1.0
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 2. Health checks pass through without issue
	recLivez := httptest.NewRecorder()
	reqLivez := httptest.NewRequest(http.MethodGet, "/livez", nil)
	handler.ServeHTTP(recLivez, reqLivez)
	if recLivez.Code != http.StatusOK {
		t.Fatalf("expected 200 for /livez, got %d", recLivez.Code)
	}
}

func TestBuildHandler_Cache(t *testing.T) {
	var backendHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"cached":true}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "cached-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		Cache: config.CacheConfig{
			Enabled:      true,
			TTL:          config.Duration(60 * time.Second),
			MaxEntries:   100,
			MaxBodyBytes: 1024 * 1024,
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	// First GET: miss, backend called
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/api/resource", nil)
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec1.Code)
	}
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected X-Cache MISS, got %q", rec1.Header().Get("X-Cache"))
	}
	if backendHits.Load() != 1 {
		t.Fatalf("expected 1 backend hit, got %d", backendHits.Load())
	}

	// Second GET: hit, backend NOT called
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/resource", nil)
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if backendHits.Load() != 1 {
		t.Fatalf("expected backend hits to remain 1 after cache hit, got %d", backendHits.Load())
	}

	// Health check bypasses cache and returns 200
	recLivez := httptest.NewRecorder()
	reqLivez := httptest.NewRequest(http.MethodGet, "/livez", nil)
	handler.ServeHTTP(recLivez, reqLivez)
	if recLivez.Code != http.StatusOK {
		t.Fatalf("expected 200 for /livez, got %d", recLivez.Code)
	}
}

func TestBuildHandler_TieredCache(t *testing.T) {
	s := miniredis.RunT(t)
	var backendHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"tiered":true}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "cached-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		Cache: config.CacheConfig{
			Enabled:      true,
			TTL:          config.Duration(60 * time.Second),
			L2Enabled:    true,
			L2TTL:        config.Duration(5 * time.Minute),
			RedisAddr:    s.Addr(),
			MaxEntries:   100,
			MaxBodyBytes: 1024 * 1024,
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	// First request: miss, calls backend and writes through to miniredis
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/api/tiered", nil)
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first status = %d, want 200", rec1.Code)
	}
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected X-Cache MISS, got %q", rec1.Header().Get("X-Cache"))
	}
	if backendHits.Load() != 1 {
		t.Fatalf("expected 1 backend hit, got %d", backendHits.Load())
	}

	// Verify key was written to Redis L2
	if !s.Exists("aegora:cache:v1:GET /api/tiered?") {
		t.Fatal("expected Redis to contain cached key")
	}

	// Second request: hit, backend NOT called
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/tiered", nil)
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200", rec2.Code)
	}
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache HIT, got %q", rec2.Header().Get("X-Cache"))
	}
	if backendHits.Load() != 1 {
		t.Fatalf("expected backend hits to remain 1 after cache hit, got %d", backendHits.Load())
	}
}

func TestBuildHandler_TieredCache_UnreachableDegrades(t *testing.T) {
	var backendHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"degraded":true}`))
	}))
	defer backend.Close()

	// Use an unreachable local port so Redis dial immediately fails
	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "cached-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
		},
		Cache: config.CacheConfig{
			Enabled:      true,
			TTL:          config.Duration(60 * time.Second),
			L2Enabled:    true,
			L2TTL:        config.Duration(5 * time.Minute),
			RedisAddr:    "127.0.0.1:65530",
			MaxEntries:   100,
			MaxBodyBytes: 1024 * 1024,
		},
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler should not fail when Redis unreachable: %v", err)
	}

	// Verify log contains warning about degrading to L1
	if !strings.Contains(logBuf.String(), "degrading to l1-only") {
		t.Errorf("expected warning log about degrading to L1, got: %s", logBuf.String())
	}

	// Verify L1 cache still works
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/api/degraded", nil)
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec1.Code)
	}
	if rec1.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected X-Cache MISS, got %q", rec1.Header().Get("X-Cache"))
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/api/degraded", nil)
	handler.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected X-Cache HIT on L1 cache, got %q", rec2.Header().Get("X-Cache"))
	}
}

func TestBuildHandler_PrometheusMetrics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"metric":true}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "metrics-test-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel:    "info",
			MetricsAddr: "127.0.0.1:9090",
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	handler, err := buildHandlerWithRegistry(cfg, logger, reg)
	if err != nil {
		t.Fatalf("buildHandlerWithRegistry: %v", err)
	}

	// Make request to matching route
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// Make request to unmatched route (404)
	rec404 := httptest.NewRecorder()
	req404 := httptest.NewRequest(http.MethodGet, "/unmatched-path", nil)
	handler.ServeHTTP(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec404.Code)
	}

	// Verify /metrics endpoint output using promhttp.HandlerFor
	metricsHandler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	metricsRec := httptest.NewRecorder()
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsHandler.ServeHTTP(metricsRec, metricsReq)

	if metricsRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", metricsRec.Code)
	}

	metricsOutput := metricsRec.Body.String()
	if !strings.Contains(metricsOutput, `aegora_requests_total{code="200",method="GET",route="metrics-test-route"} 1`) {
		t.Errorf("expected metrics to contain matching route 200 count, got:\n%s", metricsOutput)
	}
	if !strings.Contains(metricsOutput, `aegora_requests_total{code="404",method="GET",route="unmatched"} 1`) {
		t.Errorf("expected metrics to contain unmatched route 404 count, got:\n%s", metricsOutput)
	}
	if !strings.Contains(metricsOutput, `aegora_request_duration_seconds_count{method="GET",route="metrics-test-route"} 1`) {
		t.Errorf("expected duration seconds count in metrics output, got:\n%s", metricsOutput)
	}
}

func TestBuildHandler_OpenTelemetryTracing(t *testing.T) {
	var receivedTraceparent string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedTraceparent = r.Header.Get("traceparent")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"proxied"}`))
	}))
	defer backend.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Addr:         "127.0.0.1:8080",
			ReadTimeout:  config.Duration(15 * time.Second),
			WriteTimeout: config.Duration(15 * time.Second),
			IdleTimeout:  config.Duration(30 * time.Second),
		},
		Routes: []config.RouteConfig{
			{
				Name:       "trace-test-route",
				PathPrefix: "/api",
				Targets:    []string{backend.URL},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "info",
			Tracing: config.TracingConfig{
				Enabled:     true,
				Exporter:    "stdout",
				SampleRatio: 1.0,
			},
		},
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	handler, err := buildHandler(cfg, logger)
	if err != nil {
		t.Fatalf("buildHandler: %v", err)
	}

	const parentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	const parentSpanID = "00f067aa0ba902b7"
	incomingTraceparent := fmt.Sprintf("00-%s-%s-01", parentTraceID, parentSpanID)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("traceparent", incomingTraceparent)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	if receivedTraceparent == "" {
		t.Fatalf("expected backend to receive traceparent header from upstream propagation, got empty")
	}

	// Verify upstream backend received the exact same trace ID
	if !strings.Contains(receivedTraceparent, parentTraceID) {
		t.Errorf("expected propagated traceparent %q to contain parent trace ID %q", receivedTraceparent, parentTraceID)
	}
}


