package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	handler := buildHandler(cfg, logger)

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
			name:       "GET /unknown returns 501 Not Implemented",
			method:     http.MethodGet,
			path:       "/unknown",
			wantStatus: http.StatusNotImplemented,
			wantBody:   `{"error":"not implemented"}`,
		},
		{
			name:       "POST /api/v1/users returns 501 Not Implemented",
			method:     http.MethodPost,
			path:       "/api/v1/users",
			wantStatus: http.StatusNotImplemented,
			wantBody:   `{"error":"not implemented"}`,
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
	handler := buildHandler(cfg, logger)

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
	handler := buildHandler(cfg, logger)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)

	handler.ServeHTTP(rec, req)

	logOutput := buf.String()
	if logOutput == "" {
		t.Fatal("expected access log output, got empty")
	}
}
