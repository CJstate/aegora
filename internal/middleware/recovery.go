// Package middleware provides the middleware abstraction and execution
// pipeline used by Aegora to process HTTP requests.
package middleware

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recovery returns a middleware that catches panics occurring in downstream handlers,
// logs the error details, request ID, and stack trace at ERROR level, and safely returns
// an HTTP 500 Internal Server Error with a generic JSON body. The actual panic value is
// never leaked to the client.
//
// Intended chain order: Recovery outermost, then RequestID, then AccessLog.
// Recovery must be outermost to catch any panic occurring in downstream middleware
// or request handlers.
func Recovery(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					reqID := GetRequestID(r)
					stack := string(debug.Stack())

					logger.ErrorContext(r.Context(), "panic recovered",
						slog.String("request_id", reqID),
						slog.String("error", fmt.Sprintf("%v", rec)),
						slog.String("stack", stack),
					)

					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"internal server error"}`))
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}
