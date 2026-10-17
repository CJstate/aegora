package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type mockReloader struct {
	reloadFn func() (applied []string, skipped []string, err error)
}

func (m *mockReloader) Reload() (applied []string, skipped []string, err error) {
	if m.reloadFn != nil {
		return m.reloadFn()
	}
	return nil, nil, nil
}

func TestAdmin_Health(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %q", body["status"])
	}
	if body["version"] != "dev" {
		t.Errorf("expected version dev, got %q", body["version"])
	}
}

func TestAdmin_Health_MethodNotAllowed(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/health", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}
	if rec.Header().Get("Allow") != http.MethodGet {
		t.Errorf("expected Allow GET, got %q", rec.Header().Get("Allow"))
	}
}

func TestAdmin_Routes(t *testing.T) {
	testRoutes := []RouteInfo{
		{
			Name:       "api-service",
			PathPrefix: "/api",
			Targets:    []string{"http://127.0.0.1:8081"},
			LBPolicy:   "round_robin",
		},
		{
			Name:      "regex-service",
			PathRegex: `^/users/[0-9]+`,
			Targets:   []string{"http://127.0.0.1:8082"},
			LBPolicy:  "least_connections",
		},
	}

	server := NewServer("127.0.0.1:9091", nil, nil, func() []RouteInfo {
		return testRoutes
	})
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var routes []RouteInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &routes); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !reflect.DeepEqual(routes, testRoutes) {
		t.Errorf("routes mismatch:\ngot:  %+v\nwant: %+v", routes, testRoutes)
	}
}

func TestAdmin_Routes_NilReturnsEmptyArray(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, func() []RouteInfo {
		return nil
	})
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/routes", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("expected empty JSON array [], got %s", rec.Body.String())
	}
}

func TestAdmin_Config(t *testing.T) {
	cfgSnapshot := map[string]any{
		"server": map[string]any{
			"addr": "127.0.0.1:8080",
		},
		"routes": []any{
			map[string]any{"name": "test"},
		},
	}

	server := NewServer("127.0.0.1:9091", nil, func() ConfigSnapshot {
		return cfgSnapshot
	}, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/config", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	srvMap, ok := got["server"].(map[string]any)
	if !ok || srvMap["addr"] != "127.0.0.1:8080" {
		t.Errorf("expected server.addr = 127.0.0.1:8080, got %v", srvMap)
	}
}

func TestAdmin_Reload_Success(t *testing.T) {
	mock := &mockReloader{
		reloadFn: func() ([]string, []string, error) {
			return []string{"routes", "rate_limit"}, []string{"server addr"}, nil
		},
	}

	server := NewServer("127.0.0.1:9091", mock, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var res struct {
		Reloaded bool     `json:"reloaded"`
		Applied  []string `json:"applied"`
		Skipped  []string `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !res.Reloaded {
		t.Errorf("expected reloaded = true")
	}
	if !reflect.DeepEqual(res.Applied, []string{"routes", "rate_limit"}) {
		t.Errorf("applied mismatch: got %v, want [routes, rate_limit]", res.Applied)
	}
	if !reflect.DeepEqual(res.Skipped, []string{"server addr"}) {
		t.Errorf("skipped mismatch: got %v, want [server addr]", res.Skipped)
	}
}

func TestAdmin_Reload_Failure(t *testing.T) {
	mock := &mockReloader{
		reloadFn: func() ([]string, []string, error) {
			return nil, nil, errors.New("syntax error in config file")
		},
	}

	server := NewServer("127.0.0.1:9091", mock, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	var res map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	if !strings.Contains(res["error"], "syntax error in config file") {
		t.Errorf("expected error message to contain 'syntax error in config file', got %q", res["error"])
	}
}

func TestAdmin_Reload_NilReloader(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/admin/reload", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}
}

func TestAdmin_Reload_MethodNotAllowed(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/reload", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status 405, got %d", rec.Code)
	}
}

func TestAdmin_NotFound(t *testing.T) {
	server := NewServer("127.0.0.1:9091", nil, nil, nil)
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/nonexistent", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", rec.Code)
	}
}
