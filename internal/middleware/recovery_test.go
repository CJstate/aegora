package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/syncronix-labs/aegora/internal/middleware"
)

func TestRecovery_CatchesPanicAndReturns500(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	const panicMsg = "database connection failure simulation"
	var shouldPanic bool

	handler := middleware.RequestID(middleware.Recovery(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if shouldPanic {
			panic(panicMsg)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})))

	// 1. Panicking request
	shouldPanic = true
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/panic", nil)

	handler.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec1.Code)
	}

	contentType := rec1.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", contentType)
	}

	var body map[string]string
	if err := json.Unmarshal(rec1.Body.Bytes(), &body); err != nil {
		t.Fatalf("500 body is not valid JSON: %v", err)
	}
	if body["error"] != "internal server error" {
		t.Fatalf("expected error 'internal server error', got %q", body["error"])
	}
	if body["request_id"] == "" {
		t.Fatal("expected request_id in the 500 body")
	}
	if want := rec1.Header().Get("X-Request-ID"); body["request_id"] != want {
		t.Fatalf("body request_id %q does not match X-Request-ID header %q", body["request_id"], want)
	}

	// Ensure the panic value was NEVER leaked to client
	if strings.Contains(rec1.Body.String(), panicMsg) {
		t.Fatalf("panic message leaked to response body: %s", rec1.Body.String())
	}

	// Verify buffer-backed slog contains panic message, error level, and stack trace
	logOutput := buf.String()
	if !strings.Contains(logOutput, panicMsg) {
		t.Fatalf("expected log output to contain panic message %q, got: %s", panicMsg, logOutput)
	}
	if !strings.Contains(logOutput, "ERROR") {
		t.Fatalf("expected log output to be at ERROR level, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "stack") {
		t.Fatalf("expected log output to contain stack trace, got: %s", logOutput)
	}

	// 2. Subsequent normal request succeeds
	buf.Reset()
	shouldPanic = false
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/normal", nil)

	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected status 200 for normal request, got %d", rec2.Code)
	}
	if rec2.Body.String() != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", rec2.Body.String())
	}
}

func TestRecovery_IncludesRequestIDInLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	const customID = "req-panic-test-id-999"

	chain := middleware.New(
		middleware.Recovery(logger),
		middleware.RequestID,
	)

	handler := chain.Then(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("handler panic with request id")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/fail", nil)
	req.Header.Set("X-Request-ID", customID)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status 500, got %d", rec.Code)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, customID) {
		t.Fatalf("expected log output to contain request ID %q, got: %s", customID, logOutput)
	}
}
