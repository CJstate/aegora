package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestGateway_RoutingAndForwarding(t *testing.T) {
	var mu sync.Mutex
	var receivedPaths []string
	var receivedHeaders []http.Header

	backend1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedPaths = append(receivedPaths, r.URL.Path)
		receivedHeaders = append(receivedHeaders, r.Header.Clone())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"api"}`))
	}))
	defer backend1.Close()

	backend2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedPaths = append(receivedPaths, r.URL.Path)
		receivedHeaders = append(receivedHeaders, r.Header.Clone())
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"web"}`))
	}))
	defer backend2.Close()

	routes := []RouteConfig{
		{
			Name:        "api-route",
			PathPrefix:  "/api",
			Targets:     []string{backend1.URL},
			StripPrefix: "/api",
			LBPolicy:    "round_robin",
		},
		{
			Name:       "web-route",
			PathPrefix: "/web",
			Targets:    []string{backend2.URL},
			LBPolicy:   "round_robin",
		},
		{
			Name:       "unreachable-route",
			PathPrefix: "/unreachable",
			Targets:    []string{"http://127.0.0.1:1"}, // unroutable port
			LBPolicy:   "round_robin",
		},
	}

	gw, err := NewGateway(routes)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	tests := []struct {
		name           string
		method         string
		path           string
		reqHeaders     map[string]string
		wantStatusCode int
		wantBody       string
		wantPathOnUp   string
		expectUpstream bool
	}{
		{
			name:           "prefix routing to api-route with prefix stripped",
			method:         http.MethodGet,
			path:           "/api/v1/users",
			wantStatusCode: http.StatusOK,
			wantBody:       `{"service":"api"}`,
			wantPathOnUp:   "/v1/users",
			expectUpstream: true,
		},
		{
			name:           "strip prefix falls back to / when path equals strip prefix",
			method:         http.MethodGet,
			path:           "/api",
			wantStatusCode: http.StatusOK,
			wantBody:       `{"service":"api"}`,
			wantPathOnUp:   "/",
			expectUpstream: true,
		},
		{
			name:           "prefix routing without strip prefix preserves path",
			method:         http.MethodGet,
			path:           "/web/dashboard",
			wantStatusCode: http.StatusOK,
			wantBody:       `{"service":"web"}`,
			wantPathOnUp:   "/web/dashboard",
			expectUpstream: true,
		},
		{
			name:           "404 on no route matched",
			method:         http.MethodGet,
			path:           "/nonexistent/route",
			wantStatusCode: http.StatusNotFound,
			wantBody:       `{"error":"no route matched"}`,
			expectUpstream: false,
		},
		{
			name:           "502 bad gateway when backend is unreachable",
			method:         http.MethodGet,
			path:           "/unreachable/test",
			wantStatusCode: http.StatusBadGateway,
			wantBody:       `{"error":"bad gateway"}`,
			expectUpstream: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			receivedPaths = nil
			receivedHeaders = nil
			mu.Unlock()

			req := httptest.NewRequest(tt.method, tt.path, nil)
			for k, v := range tt.reqHeaders {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()

			gw.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatusCode {
				t.Fatalf("status code mismatch: got %d, want %d", rec.Code, tt.wantStatusCode)
			}

			if rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("expected application/json Content-Type, got %q", rec.Header().Get("Content-Type"))
			}

			body := rec.Body.String()
			if body != tt.wantBody {
				t.Fatalf("body mismatch: got %q, want %q", body, tt.wantBody)
			}

			if tt.expectUpstream {
				mu.Lock()
				count := len(receivedPaths)
				var lastPath string
				if count > 0 {
					lastPath = receivedPaths[count-1]
				}
				mu.Unlock()

				if count == 0 {
					t.Fatal("expected request to reach upstream backend, but none recorded")
				}
				if lastPath != tt.wantPathOnUp {
					t.Fatalf("upstream path mismatch: got %q, want %q", lastPath, tt.wantPathOnUp)
				}
			}
		})
	}
}

func TestGateway_HeaderSanitizationAndAegoraHeader(t *testing.T) {
	var capturedHeader http.Header
	var mu sync.Mutex

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		capturedHeader = r.Header.Clone()
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	routes := []RouteConfig{
		{
			Name:       "test-route",
			PathPrefix: "/test",
			Targets:    []string{backend.URL},
		},
	}

	gw, err := NewGateway(routes)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/test/path", nil)
	req.Header.Set("X-Forwarded-For", "spoofed-client-ip")
	req.Header.Set("X-Forwarded-Host", "spoofed-host")
	req.Header.Set("X-Forwarded-Proto", "spoofed-proto")
	req.Header.Set("X-Aegora", "false")

	rec := httptest.NewRecorder()
	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	mu.Lock()
	header := capturedHeader
	mu.Unlock()

	if header.Get("X-Aegora") != "true" {
		t.Fatalf("expected X-Aegora header to be 'true', got %q", header.Get("X-Aegora"))
	}
	if header.Get("X-Forwarded-For") == "spoofed-client-ip" {
		t.Fatal("spoofed X-Forwarded-For header was not stripped")
	}
}

func TestGateway_AllUpstreamsUnavailable(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:       "failover-route",
			PathPrefix: "/failover",
			Targets:    []string{"http://127.0.0.1:8081"},
			LBPolicy:   "round_robin",
		},
	}

	gw, err := NewGateway(routes)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	// Mark the only target as failed
	for _, entry := range gw.routes {
		entry.pool.ReportFailure(0)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/failover/item", nil)

	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 Bad Gateway, got %d", rec.Code)
	}

	if rec.Body.String() != `{"error":"all upstreams unavailable"}` {
		t.Fatalf("expected 'all upstreams unavailable' error body, got %q", rec.Body.String())
	}

	// Verify valid JSON
	var parsed map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}
	if parsed["error"] != "all upstreams unavailable" {
		t.Fatalf("expected error 'all upstreams unavailable', got %q", parsed["error"])
	}
}

func TestGateway_UnreachableBackendReportsFailure(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:       "bad-backend",
			PathPrefix: "/bad",
			Targets:    []string{"http://127.0.0.1:1"},
			LBPolicy:   "round_robin",
		},
	}

	gw, err := NewGateway(routes)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/bad/action", nil)

	gw.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if rec.Body.String() != `{"error":"bad gateway"}` {
		t.Fatalf("expected 'bad gateway', got %q", rec.Body.String())
	}

	// Target should now be marked unhealthy
	for _, entry := range gw.routes {
		_, _, ok := entry.pool.Next()
		if ok {
			t.Fatal("expected pool to mark target unhealthy after backend failure")
		}
	}
}

func TestGateway_NewGatewayInvalidRoute(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:       "bad-route",
			PathPrefix: "/bad",
			Targets:    []string{"not-a-valid-url"},
		},
	}

	_, err := NewGateway(routes)
	if err == nil {
		t.Fatal("expected NewGateway error for invalid target URL, got nil")
	}
}

func TestGateway_NilGatewayOrRouter(t *testing.T) {
	var gw *Gateway
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	gw.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestGateway_DoesNotLeakBackendError(t *testing.T) {
	routes := []RouteConfig{
		{
			Name:       "unreachable",
			PathPrefix: "/test",
			Targets:    []string{"http://127.0.0.1:1"},
		},
	}

	gw, err := NewGateway(routes)
	if err != nil {
		t.Fatalf("NewGateway: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)

	gw.ServeHTTP(rec, req)

	body, _ := io.ReadAll(rec.Body)
	// Must strictly equal `{"error":"bad gateway"}` and never contain dial tcp or connection refused
	if string(body) != `{"error":"bad gateway"}` {
		t.Fatalf("leaked backend error details: %s", string(body))
	}
}
