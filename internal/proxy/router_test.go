package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/syncronix-labs/aegora/internal/proxy"
)

func TestRouter_LongestPrefixWins(t *testing.T) {
	// Registered in arbitrary order: shorter prefixes before longer ones
	routes := []proxy.RouteConfig{
		{
			Name:       "root-route",
			PathPrefix: "/",
			Targets:    []string{"http://127.0.0.1:8000"},
		},
		{
			Name:       "api-route",
			PathPrefix: "/api",
			Targets:    []string{"http://127.0.0.1:8001"},
		},
		{
			Name:       "api-v1-route",
			PathPrefix: "/api/v1",
			Targets:    []string{"http://127.0.0.1:8002"},
		},
		{
			Name:       "api-v1-users-route",
			PathPrefix: "/api/v1/users",
			Targets:    []string{"http://127.0.0.1:8003"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error from NewRouter: %v", err)
	}

	tests := []struct {
		name      string
		path      string
		wantRoute string
	}{
		{
			name:      "longest prefix matches /api/v1/users/42",
			path:      "/api/v1/users/42",
			wantRoute: "api-v1-users-route",
		},
		{
			name:      "exact match for /api/v1/users",
			path:      "/api/v1/users",
			wantRoute: "api-v1-users-route",
		},
		{
			name:      "intermediate prefix matches /api/v1/orders",
			path:      "/api/v1/orders",
			wantRoute: "api-v1-route",
		},
		{
			name:      "shorter prefix matches /api/health",
			path:      "/api/health",
			wantRoute: "api-route",
		},
		{
			name:      "root prefix matches /dashboard",
			path:      "/dashboard",
			wantRoute: "root-route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			matched := router.Match(req)
			if matched == nil {
				t.Fatalf("expected match for %s, got nil", tt.path)
			}
			if matched.Name != tt.wantRoute {
				t.Fatalf("path %s: got route %q, want %q", tt.path, matched.Name, tt.wantRoute)
			}
		})
	}
}

func TestRouter_RegexBeatsPrefixPrecedence(t *testing.T) {
	// Prefix declared first, regex declared second.
	// Precedence rules dictate regex must beat prefix regardless of declaration order.
	routes := []proxy.RouteConfig{
		{
			Name:       "prefix-users-route",
			PathPrefix: "/users",
			Targets:    []string{"http://127.0.0.1:8001"},
		},
		{
			Name:             "regex-numeric-user-route",
			PathPrefix:       "/users",
			PathRegexPattern: `^/users/\d+$`,
			Targets:          []string{"http://127.0.0.1:8002"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error from NewRouter: %v", err)
	}

	tests := []struct {
		name      string
		path      string
		wantRoute string
	}{
		{
			name:      "numeric user matches regex route over prefix route",
			path:      "/users/12345",
			wantRoute: "regex-numeric-user-route",
		},
		{
			name:      "alphanumeric user fails regex and falls back to prefix route",
			path:      "/users/john-doe",
			wantRoute: "prefix-users-route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			matched := router.Match(req)
			if matched == nil {
				t.Fatalf("expected match for %s, got nil", tt.path)
			}
			if matched.Name != tt.wantRoute {
				t.Fatalf("path %s: got route %q, want %q", tt.path, matched.Name, tt.wantRoute)
			}
		})
	}
}

func TestRouter_MethodFiltering(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:       "get-items-route",
			PathPrefix: "/items",
			Methods:    []string{"GET"},
			Targets:    []string{"http://127.0.0.1:8001"},
		},
		{
			Name:       "post-items-route",
			PathPrefix: "/items",
			Methods:    []string{"POST"},
			Targets:    []string{"http://127.0.0.1:8002"},
		},
		{
			Name:       "all-other-items-route",
			PathPrefix: "/items",
			Targets:    []string{"http://127.0.0.1:8003"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error from NewRouter: %v", err)
	}

	tests := []struct {
		name      string
		method    string
		path      string
		wantRoute string
	}{
		{
			name:      "GET /items matches get-items-route",
			method:    http.MethodGet,
			path:      "/items",
			wantRoute: "get-items-route",
		},
		{
			name:      "POST /items matches post-items-route",
			method:    http.MethodPost,
			path:      "/items",
			wantRoute: "post-items-route",
		},
		{
			name:      "DELETE /items matches fallback all-other-items-route",
			method:    http.MethodDelete,
			path:      "/items",
			wantRoute: "all-other-items-route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			matched := router.Match(req)
			if matched == nil {
				t.Fatalf("expected match for %s %s, got nil", tt.method, tt.path)
			}
			if matched.Name != tt.wantRoute {
				t.Fatalf("got route %q, want %q", matched.Name, tt.wantRoute)
			}
		})
	}
}

func TestRouter_HeaderRules(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:       "beta-v2-route",
			PathPrefix: "/service",
			Headers: map[string]string{
				"X-Beta":    "true",
				"X-Version": "v2",
			},
			Targets: []string{"http://127.0.0.1:8001"},
		},
		{
			Name:       "default-service-route",
			PathPrefix: "/service",
			Targets:    []string{"http://127.0.0.1:8002"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error from NewRouter: %v", err)
	}

	tests := []struct {
		name      string
		headers   map[string]string
		wantRoute string
	}{
		{
			name: "all required headers match beta route",
			headers: map[string]string{
				"X-Beta":    "true",
				"X-Version": "v2",
			},
			wantRoute: "beta-v2-route",
		},
		{
			name: "partial header match falls back to default route",
			headers: map[string]string{
				"X-Beta": "true",
			},
			wantRoute: "default-service-route",
		},
		{
			name: "wrong header value falls back to default route",
			headers: map[string]string{
				"X-Beta":    "true",
				"X-Version": "v1",
			},
			wantRoute: "default-service-route",
		},
		{
			name:      "no headers provided falls back to default route",
			headers:   nil,
			wantRoute: "default-service-route",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/service/data", nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			matched := router.Match(req)
			if matched == nil {
				t.Fatalf("expected match, got nil")
			}
			if matched.Name != tt.wantRoute {
				t.Fatalf("got route %q, want %q", matched.Name, tt.wantRoute)
			}
		})
	}
}

func TestRouter_NoMatchReturnsNil(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:       "api-only-route",
			PathPrefix: "/api",
			Methods:    []string{"GET"},
			Targets:    []string{"http://127.0.0.1:8001"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. Path doesn't match
	req1 := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	if matched := router.Match(req1); matched != nil {
		t.Fatalf("expected nil match for /dashboard, got %q", matched.Name)
	}

	// 2. Method doesn't match
	req2 := httptest.NewRequest(http.MethodPost, "/api/data", nil)
	if matched := router.Match(req2); matched != nil {
		t.Fatalf("expected nil match for POST /api/data, got %q", matched.Name)
	}

	// 3. Nil request safety
	if matched := router.Match(nil); matched != nil {
		t.Fatalf("expected nil for nil request, got %v", matched)
	}
}

func TestNewRouter_InvalidRegexNamesRoute(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:             "broken-regex-route",
			PathPrefix:       "/api",
			PathRegexPattern: "[unclosed-character-class",
			Targets:          []string{"http://127.0.0.1:8001"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err == nil {
		t.Fatal("expected error from NewRouter for invalid regex, got nil")
	}

	if router != nil {
		t.Fatalf("expected router to be nil on error, got %v", router)
	}

	if !strings.Contains(err.Error(), "broken-regex-route") {
		t.Fatalf("expected error message to name the route %q, got %v", "broken-regex-route", err)
	}

	if !strings.Contains(err.Error(), "[unclosed-character-class") {
		t.Fatalf("expected error message to contain invalid regex pattern, got %v", err)
	}
}

func TestNewRouter_MissingTargetsNamesRoute(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:       "no-targets-route",
			PathPrefix: "/api",
			Targets:    nil,
		},
	}

	router, err := proxy.NewRouter(routes)
	if err == nil {
		t.Fatal("expected error from NewRouter for missing targets, got nil")
	}

	if router != nil {
		t.Fatalf("expected router to be nil on error, got %v", router)
	}

	if !strings.Contains(err.Error(), "no-targets-route") {
		t.Fatalf("expected error message to name the route %q, got %v", "no-targets-route", err)
	}
}

func TestRouter_EscapedPathMatching(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:       "encoded-route",
			PathPrefix: "/files/a%2Fb",
			Targets:    []string{"http://127.0.0.1:8001"},
		},
		{
			Name:       "plain-route",
			PathPrefix: "/files/a/b",
			Targets:    []string{"http://127.0.0.1:8002"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request with percent-encoded %2F
	req := httptest.NewRequest(http.MethodGet, "/files/a%2Fb/sub", nil)
	matched := router.Match(req)
	if matched == nil {
		t.Fatal("expected match for escaped path request, got nil")
	}
	if matched.Name != "encoded-route" {
		t.Fatalf("expected encoded-route, got %q", matched.Name)
	}
}

func TestRouter_PrecompiledRegex(t *testing.T) {
	routes := []proxy.RouteConfig{
		{
			Name:      "precompiled-route",
			PathRegex: regexp.MustCompile(`^/items/[a-z]+$`),
			Targets:   []string{"http://127.0.0.1:8001"},
		},
	}

	router, err := proxy.NewRouter(routes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/items/widget", nil)
	matched := router.Match(req)
	if matched == nil || matched.Name != "precompiled-route" {
		t.Fatalf("expected match for precompiled route, got %v", matched)
	}

	if len(router.Routes()) != 1 {
		t.Fatalf("expected 1 route, got %d", len(router.Routes()))
	}
}
