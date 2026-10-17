// Package middleware provides the middleware abstraction and execution
// pipeline used by Aegora to process HTTP requests.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

type contextKeyRequestID struct{}

const headerRequestID = "X-Request-ID"

// RequestID returns an HTTP middleware that manages the X-Request-ID header and context value.
// If the incoming request already carries an X-Request-ID header, that value is preserved;
// otherwise 16 cryptographically random bytes are hex-encoded into a new 32-character ID.
// The ID is written to the response header and stored in the request's context.
//
// Intended chain order: Recovery outermost, then RequestID, then AccessLog.
// RequestID must run before AccessLog so the log line has the ID.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get(headerRequestID)
		if reqID == "" {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				reqID = "00000000000000000000000000000000"
			} else {
				reqID = hex.EncodeToString(b[:])
			}
		}

		w.Header().Set(headerRequestID, reqID)

		ctx := context.WithValue(r.Context(), contextKeyRequestID{}, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID retrieves the request ID from the request context,
// or falls back to checking the X-Request-ID header on the request.
// Returns an empty string if no request ID is present.
func GetRequestID(r *http.Request) string {
	if r == nil {
		return ""
	}

	if id, ok := r.Context().Value(contextKeyRequestID{}).(string); ok && id != "" {
		return id
	}

	return r.Header.Get(headerRequestID)
}
