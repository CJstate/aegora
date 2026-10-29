package httperr_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/syncronix-labs/aegora/internal/httperr"
)

func TestWrite(t *testing.T) {
	rec := httptest.NewRecorder()

	httperr.Write(rec, http.StatusBadGateway, "bad gateway", "abc123")

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if body["error"] != "bad gateway" {
		t.Errorf("error = %q, want %q", body["error"], "bad gateway")
	}
	if body["request_id"] != "abc123" {
		t.Errorf("request_id = %q, want %q", body["request_id"], "abc123")
	}
}

// TestWrite_EmptyRequestIDKeepsTheField pins the contract that request_id is
// always present, so clients do not have to treat a missing key and an empty
// value differently.
func TestWrite_EmptyRequestIDKeepsTheField(t *testing.T) {
	rec := httptest.NewRecorder()

	httperr.Write(rec, http.StatusNotFound, "no route matched", "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	if _, ok := raw["request_id"]; !ok {
		t.Fatal("request_id key is missing from the body")
	}
}
