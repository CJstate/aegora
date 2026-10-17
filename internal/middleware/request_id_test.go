package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syncronix-labs/aegora/internal/middleware"
)

func TestRequestID_GeneratesUniqueIDs(t *testing.T) {
	var capturedID1, capturedID2 string

	handler1 := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID1 = middleware.GetRequestID(r)
		w.WriteHeader(http.StatusOK)
	}))

	req1 := httptest.NewRequest(http.MethodGet, "/test1", nil)
	rec1 := httptest.NewRecorder()
	handler1.ServeHTTP(rec1, req1)

	respID1 := rec1.Header().Get("X-Request-ID")
	if respID1 == "" {
		t.Fatal("expected non-empty X-Request-ID response header for request 1")
	}
	if capturedID1 != respID1 {
		t.Fatalf("expected context request ID %q to match response header %q", capturedID1, respID1)
	}
	if len(respID1) != 32 {
		t.Fatalf("expected 32 hex chars for 16-byte ID, got len %d (%q)", len(respID1), respID1)
	}

	handler2 := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID2 = middleware.GetRequestID(r)
		w.WriteHeader(http.StatusOK)
	}))

	req2 := httptest.NewRequest(http.MethodGet, "/test2", nil)
	rec2 := httptest.NewRecorder()
	handler2.ServeHTTP(rec2, req2)

	respID2 := rec2.Header().Get("X-Request-ID")
	if respID2 == "" {
		t.Fatal("expected non-empty X-Request-ID response header for request 2")
	}
	if capturedID2 != respID2 {
		t.Fatalf("expected context request ID %q to match response header %q", capturedID2, respID2)
	}

	if respID1 == respID2 {
		t.Fatalf("expected two different requests to receive unique IDs, got identical %q", respID1)
	}
}

func TestRequestID_EchoesIncomingHeader(t *testing.T) {
	const customID = "custom-client-trace-id-12345"
	var capturedID string

	handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = middleware.GetRequestID(r)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/echo", nil)
	req.Header.Set("X-Request-ID", customID)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	respID := rec.Header().Get("X-Request-ID")
	if respID != customID {
		t.Fatalf("expected response header X-Request-ID to be %q, got %q", customID, respID)
	}
	if capturedID != customID {
		t.Fatalf("expected GetRequestID(r) to return %q, got %q", customID, capturedID)
	}
}

func TestGetRequestID_NilOrEmpty(t *testing.T) {
	if got := middleware.GetRequestID(nil); got != "" {
		t.Fatalf("expected empty string for nil request, got %q", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := middleware.GetRequestID(req); got != "" {
		t.Fatalf("expected empty string for raw request without middleware, got %q", got)
	}
}
