package reliability

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy defines the parameters governing request retry attempts and backoff timing.
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts allowed (initial request + retries).
	MaxAttempts int
	// InitialDelay is the base backoff duration for the first retry.
	InitialDelay time.Duration
	// MaxDelay is the maximum backoff duration permitted.
	MaxDelay time.Duration
	// Multiplier is the exponential growth factor applied across successive attempts.
	Multiplier float64
	// Jitter is the degree of randomness in [0.0, 1.0] applied to backoff delays.
	// A value of 1.0 represents full jitter.
	Jitter float64
}

// Validate verifies that the RetryPolicy parameters satisfy operational constraints.
// It requires MaxAttempts >= 1, positive delays with InitialDelay <= MaxDelay,
// Multiplier >= 1.0, and Jitter within [0.0, 1.0].
func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return errors.New("reliability: max attempts must be at least 1")
	}
	if p.InitialDelay <= 0 {
		return errors.New("reliability: initial delay must be positive")
	}
	if p.MaxDelay <= 0 {
		return errors.New("reliability: max delay must be positive")
	}
	if p.InitialDelay > p.MaxDelay {
		return errors.New("reliability: initial delay cannot exceed max delay")
	}
	if p.Multiplier < 1.0 {
		return errors.New("reliability: multiplier must be at least 1.0")
	}
	if p.Jitter < 0.0 || p.Jitter > 1.0 {
		return errors.New("reliability: jitter must be between 0.0 and 1.0")
	}
	return nil
}

// Backoff computes the sleep duration for the given 0-indexed retry attempt.
// Under full jitter (Jitter == 1.0), the duration is uniformly distributed
// within [0, min(MaxDelay, InitialDelay * Multiplier^attempt)].
func (p RetryPolicy) Backoff(attempt int) time.Duration {
	if p.InitialDelay <= 0 {
		return 0
	}
	mult := p.Multiplier
	if mult < 1.0 {
		mult = 1.0
	}

	ceiling := float64(p.InitialDelay) * math.Pow(mult, float64(attempt))
	if p.MaxDelay > 0 && ceiling > float64(p.MaxDelay) {
		ceiling = float64(p.MaxDelay)
	}

	if p.Jitter <= 0 {
		return time.Duration(ceiling)
	}

	jitter := p.Jitter
	if jitter > 1.0 {
		jitter = 1.0
	}

	minDelay := ceiling * (1.0 - jitter)
	delta := ceiling - minDelay
	sleep := minDelay + rand.Float64()*delta
	return time.Duration(sleep)
}

// parseRetryAfter parses standard HTTP Retry-After headers specified either as
// integer delta-seconds (RFC 9110 §10.2.3) or as an HTTP-date.
func parseRetryAfter(header string, now time.Time) (time.Duration, bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, false
	}

	// Try parsing as integer seconds
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds < 0 {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}

	// Try parsing as HTTP-date (RFC 1123, RFC 850, ANSI C)
	for _, layout := range []string{http.TimeFormat, time.RFC850, time.ANSIC} {
		if t, err := time.Parse(layout, header); err == nil {
			dur := t.Sub(now)
			if dur < 0 {
				return 0, true
			}
			return dur, true
		}
	}

	return 0, false
}

// isIdempotentMethod reports whether the HTTP method is idempotent according to RFC 9110 §9.2.2.
// GET, HEAD, and OPTIONS are safe/idempotent; PUT and DELETE are idempotent state mutations.
// POST and PATCH are explicitly non-idempotent and must never be retried automatically.
func isIdempotentMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions:
		return true
	default:
		return false
	}
}

// isRetryableStatus reports whether the HTTP response status qualifies for automatic retry.
// Only gateway and availability errors (502 Bad Gateway, 503 Service Unavailable,
// and 504 Gateway Timeout) are retryable. Application errors (such as 500 Internal Server Error)
// and client errors (4xx) are not retried.
func isRetryableStatus(code int) bool {
	return code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// RetryTransport wraps an http.RoundTripper with automatic retry and backoff logic.
type RetryTransport struct {
	Base   http.RoundTripper
	Policy RetryPolicy
}

// RoundTrip executes the request, retrying idempotent requests on transport errors
// (except context cancellation) or retryable HTTP responses (502, 503, 504).
//
// Idempotency constraint:
// Retries are restricted to GET, HEAD, PUT, DELETE, and OPTIONS. POST and PATCH are
// never retried automatically because repeating a non-idempotent request could result
// in duplicate state mutations, duplicate billing, or non-deterministic data corruption.
//
// Body safety constraint:
// Retrying is only safe when req.Body == nil or req.GetBody != nil. A stream-based request
// body without a GetBody constructor is consumed and closed on the first attempt by the
// underlying transport and cannot be rewound; retrying would transmit an empty or truncated
// body. In such cases, only a single attempt is made.
func (rt *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := rt.Base
	if base == nil {
		base = http.DefaultTransport
	}

	maxAttempts := rt.Policy.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	// Enforce method idempotency: never retry POST or PATCH.
	if !isIdempotentMethod(req.Method) {
		maxAttempts = 1
	}

	// Enforce body safety: if body is non-nil and cannot be rewound, allow only 1 attempt.
	if req.Body != nil && req.GetBody == nil {
		maxAttempts = 1
	}

	var lastResp *http.Response
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Return immediately if the request context is already cancelled.
		if err := req.Context().Err(); err != nil {
			return nil, err
		}

		attemptReq := req
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("reliability: retry get body: %w", err)
			}
			attemptReq = req.Clone(req.Context())
			attemptReq.Body = body
		}

		resp, err := base.RoundTrip(attemptReq)
		switch {
		case err != nil:
			// Abort immediately on context cancellation or deadline expiration.
			if req.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			lastErr = err
			lastResp = nil
		case isRetryableStatus(resp.StatusCode):
			lastErr = nil
			lastResp = resp
		default:
			// Non-retryable status code (e.g. 2xx, 3xx, 4xx, 500, 501).
			return resp, nil
		}

		// If no further attempts remain, do not drain or sleep; return the result.
		if attempt+1 >= maxAttempts {
			break
		}

		// Drain up to 4KB and close the response body of the failed attempt for TCP reuse.
		var retryAfterHeader string
		if lastResp != nil {
			retryAfterHeader = lastResp.Header.Get("Retry-After")
			if lastResp.Body != nil {
				const maxDrainBytes = 4096
				_, _ = io.Copy(io.Discard, io.LimitReader(lastResp.Body, maxDrainBytes))
				_ = lastResp.Body.Close()
			}
		}

		// Compute backoff duration, respecting parseable Retry-After up to MaxDelay.
		var delay time.Duration
		if ra, ok := parseRetryAfter(retryAfterHeader, time.Now()); ok {
			delay = ra
			if rt.Policy.MaxDelay > 0 && delay > rt.Policy.MaxDelay {
				delay = rt.Policy.MaxDelay
			}
		} else {
			delay = rt.Policy.Backoff(attempt)
		}

		// Sleep abortable via request context.
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		} else {
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			default:
			}
		}
	}

	return lastResp, lastErr
}
