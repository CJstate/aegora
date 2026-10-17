// Package middleware provides the middleware abstraction and execution
// pipeline used by Aegora to process HTTP requests.
package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

type responseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}

	rw.status = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}

	return rw.ResponseWriter.Write(b)
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// AccessLog returns a middleware that emits structured access logs at the INFO level
// using the provided slog.Logger.
//
// Exactly one log line is produced per completed request containing:
//   - method (string)
//   - path (string)
//   - status (int)
//   - latency_ms (float64)
//   - request_id (string, retrieved via GetRequestID)
//
// Intended chain order: Recovery outermost, then RequestID, then AccessLog.
// RequestID must run before AccessLog so the log line has the ID.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := newResponseWriter(w)

			next.ServeHTTP(rw, r)

			latency := time.Since(start)
			latencyMs := float64(latency.Nanoseconds()) / 1e6
			if latencyMs <= 0 {
				latencyMs = 0.001
			}

			logger.InfoContext(r.Context(), "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Float64("latency_ms", latencyMs),
				slog.String("request_id", GetRequestID(r)),
			)
		})
	}
}
