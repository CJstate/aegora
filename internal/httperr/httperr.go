// Package httperr renders the JSON error bodies that Aegora generates itself.
//
// Every gateway-generated error carries the request ID so a client can quote it
// in a bug report and an operator can jump straight to the matching access log
// line instead of correlating on timestamps.
package httperr

import (
	"encoding/json"
	"net/http"
)

// Response is the wire format of a gateway-generated JSON error.
type Response struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id"`
}

// Write writes status and the JSON error body to w.
//
// requestID is the value from middleware.GetRequestID for the same request. The
// field is always rendered, even as the empty string, so clients that parse it
// do not have to treat a missing key differently from an empty one.
func Write(w http.ResponseWriter, status int, message, requestID string) {
	body, err := json.Marshal(Response{Error: message, RequestID: requestID})
	if err != nil {
		// Response only holds strings, so this is unreachable; fall back to a
		// valid JSON body rather than writing nothing.
		body = []byte(`{"error":"internal server error","request_id":""}`)
		status = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
