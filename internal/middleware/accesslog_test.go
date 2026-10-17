package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/syncronix-labs/aegora/internal/middleware"
)

type accessLogJSON struct {
	Level     string  `json:"level"`
	Msg       string  `json:"msg"`
	Method    string  `json:"method"`
	Path      string  `json:"path"`
	Status    int     `json:"status"`
	LatencyMs float64 `json:"latency_ms"`
	RequestID string  `json:"request_id"`
}

func TestAccessLog_ProducesExactJSONLine(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	const customReqID = "client-access-test-456"

	chain := middleware.New(
		middleware.RequestID,
		middleware.AccessLog(logger),
	)

	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond) // ensure measurable latency
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("X-Request-ID", customReqID)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	output := strings.TrimSpace(buf.String())
	lines := strings.Split(output, "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one log line, got %d lines: %s", len(lines), output)
	}

	var entry accessLogJSON
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("failed to parse access log JSON: %v, raw output: %s", err, lines[0])
	}

	if entry.Level != "INFO" {
		t.Errorf("expected level INFO, got %q", entry.Level)
	}
	if entry.Method != "GET" {
		t.Errorf("expected method GET, got %q", entry.Method)
	}
	if entry.Path != "/api/v1/users" {
		t.Errorf("expected path /api/v1/users, got %q", entry.Path)
	}
	if entry.Status != http.StatusOK {
		t.Errorf("expected status 200, got %d", entry.Status)
	}
	if entry.LatencyMs <= 0 {
		t.Errorf("expected latency_ms > 0, got %f", entry.LatencyMs)
	}
	if entry.RequestID != customReqID {
		t.Errorf("expected request_id %q, got %q", customReqID, entry.RequestID)
	}
}

func TestAccessLog_LogsStatus403(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	chain := middleware.New(
		middleware.RequestID,
		middleware.AccessLog(logger),
	)

	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("forbidden"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/admin/dashboard", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected HTTP 403, got %d", rec.Code)
	}

	output := strings.TrimSpace(buf.String())
	var entry accessLogJSON
	if err := json.Unmarshal([]byte(output), &entry); err != nil {
		t.Fatalf("failed to parse JSON log line: %v", err)
	}

	if entry.Method != "POST" {
		t.Errorf("expected method POST, got %q", entry.Method)
	}
	if entry.Path != "/admin/dashboard" {
		t.Errorf("expected path /admin/dashboard, got %q", entry.Path)
	}
	if entry.Status != http.StatusForbidden {
		t.Errorf("expected status 403, got %d", entry.Status)
	}
	if entry.RequestID == "" {
		t.Error("expected non-empty generated request_id")
	}
}

func TestAccessLog_DefaultsTo200WithoutExplicitWriteHeader(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	chain := middleware.New(
		middleware.RequestID,
		middleware.AccessLog(logger),
	)

	// Handler only writes body without explicit WriteHeader call
	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("implicit 200 response"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/default-status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var entry accessLogJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("failed to parse JSON log line: %v", err)
	}

	if entry.Status != http.StatusOK {
		t.Errorf("expected default status 200, got %d", entry.Status)
	}
}

func TestChainOrder_RecoveryThenRequestIDThenAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	// Recommended full stack chain: Recovery outermost, then RequestID, then AccessLog
	chain := middleware.New(
		middleware.Recovery(logger),
		middleware.RequestID,
		middleware.AccessLog(logger),
	)

	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chain ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header on response")
	}

	var entry accessLogJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &entry); err != nil {
		t.Fatalf("failed to parse access log JSON: %v", err)
	}

	if entry.RequestID != rec.Header().Get("X-Request-ID") {
		t.Fatalf("expected logged request_id %q to match response header %q", entry.RequestID, rec.Header().Get("X-Request-ID"))
	}
}
