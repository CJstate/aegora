package reliability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryPolicy_Validate(t *testing.T) {
	valid := RetryPolicy{
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Multiplier:   2.0,
		Jitter:       1.0,
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid policy, got %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*RetryPolicy)
	}{
		{
			name:   "zero max attempts",
			mutate: func(p *RetryPolicy) { p.MaxAttempts = 0 },
		},
		{
			name:   "negative max attempts",
			mutate: func(p *RetryPolicy) { p.MaxAttempts = -1 },
		},
		{
			name:   "zero initial delay",
			mutate: func(p *RetryPolicy) { p.InitialDelay = 0 },
		},
		{
			name:   "negative initial delay",
			mutate: func(p *RetryPolicy) { p.InitialDelay = -time.Millisecond },
		},
		{
			name:   "zero max delay",
			mutate: func(p *RetryPolicy) { p.MaxDelay = 0 },
		},
		{
			name:   "negative max delay",
			mutate: func(p *RetryPolicy) { p.MaxDelay = -time.Millisecond },
		},
		{
			name:   "initial delay exceeds max delay",
			mutate: func(p *RetryPolicy) { p.InitialDelay = 200 * time.Millisecond; p.MaxDelay = 100 * time.Millisecond },
		},
		{
			name:   "multiplier less than 1.0",
			mutate: func(p *RetryPolicy) { p.Multiplier = 0.99 },
		},
		{
			name:   "negative jitter",
			mutate: func(p *RetryPolicy) { p.Jitter = -0.1 },
		},
		{
			name:   "jitter greater than 1.0",
			mutate: func(p *RetryPolicy) { p.Jitter = 1.1 },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatalf("expected validation error for %s, got nil", tt.name)
			}
		})
	}
}

func TestRetryTransport_FailsTwiceThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n < 3 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(bytes.NewBufferString("service unavailable")),
				Header:     make(http.Header),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("success")),
			Header:     make(http.Header),
		}, nil
	})

	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0.1,
		},
	}

	req, err := http.NewRequest(http.MethodGet, "http://example.com/items", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected exactly 3 calls, got %d", got)
	}
}

func TestRetryTransport_GivesUpAfterMaxAttempts(t *testing.T) {
	t.Run("gives up on 503 response", func(t *testing.T) {
		var calls atomic.Int32
		stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(bytes.NewBufferString("down")),
				Header:     make(http.Header),
			}, nil
		})

		rt := &RetryTransport{
			Base: stub,
			Policy: RetryPolicy{
				MaxAttempts:  3,
				InitialDelay: 2 * time.Millisecond,
				MaxDelay:     10 * time.Millisecond,
				Multiplier:   2.0,
				Jitter:       0,
			},
		}

		req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("expected nil error on 503, got %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("expected status 503, got %d", resp.StatusCode)
		}
		if got := calls.Load(); got != 3 {
			t.Fatalf("expected exactly 3 calls before giving up, got %d", got)
		}
	})

	t.Run("gives up on transport error", func(t *testing.T) {
		var calls atomic.Int32
		stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("connection reset")
		})

		rt := &RetryTransport{
			Base: stub,
			Policy: RetryPolicy{
				MaxAttempts:  3,
				InitialDelay: 2 * time.Millisecond,
				MaxDelay:     10 * time.Millisecond,
				Multiplier:   2.0,
				Jitter:       0,
			},
		}

		req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
		resp, err := rt.RoundTrip(req)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Fatal("expected transport error, got nil")
		}
		if resp != nil {
			t.Fatalf("expected nil response, got %v", resp)
		}
		if got := calls.Load(); got != 3 {
			t.Fatalf("expected exactly 3 calls before giving up, got %d", got)
		}
	})
}

func TestRetryTransport_NoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       io.NopCloser(bytes.NewBufferString("bad request")),
			Header:     make(http.Header),
		}, nil
	})

	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0,
		},
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/bad", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected nil error on 400, got %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 400 not to be retried; got %d calls, want 1", got)
	}
}

func TestRetryTransport_POSTNeverRetried(t *testing.T) {
	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Body:       io.NopCloser(bytes.NewBufferString("bad gateway")),
			Header:     make(http.Header),
		}, nil
	})

	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  4,
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0,
		},
	}

	req, _ := http.NewRequest(http.MethodPost, "http://example.com/submit", bytes.NewBufferString(`{"data":1}`))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected POST never to be retried; got %d calls, want 1", got)
	}
}

func TestRetryTransport_PATCHNeverRetried(t *testing.T) {
	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("dial error")
	})

	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     10 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0,
		},
	}

	req, _ := http.NewRequest(http.MethodPatch, "http://example.com/patch", bytes.NewBufferString(`{"op":"replace"}`))
	resp, err := rt.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected PATCH never to be retried; got %d calls, want 1", got)
	}
}

func TestRetryTransport_ContextCancelAbortsMidBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		// Cancel context immediately during the 1st request, so backoff timer is aborted
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(bytes.NewBufferString("down")),
			Header:     make(http.Header),
		}, nil
	})

	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  3,
			InitialDelay: 500 * time.Millisecond, // Long backoff to test cancellation abort
			MaxDelay:     time.Second,
			Multiplier:   2.0,
			Jitter:       0,
		},
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/ctx", nil)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	duration := time.Since(start)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected only 1 call before context cancellation aborted backoff, got %d", calls.Load())
	}
	if duration >= 400*time.Millisecond {
		t.Fatalf("expected early abort well before initial delay, took %v", duration)
	}
}

func TestRetryTransport_RetryAfterHeaderHonored(t *testing.T) {
	var calls atomic.Int32
	stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			hdr := make(http.Header)
			// Server asks for 1 second retry delay
			hdr.Set("Retry-After", "1")
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(bytes.NewBufferString("retry after")),
				Header:     hdr,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewBufferString("ok")),
			Header:     make(http.Header),
		}, nil
	})

	// Capping MaxDelay at 40ms tests that Retry-After is respected and capped at MaxDelay
	rt := &RetryTransport{
		Base: stub,
		Policy: RetryPolicy{
			MaxAttempts:  2,
			InitialDelay: 2 * time.Millisecond,
			MaxDelay:     40 * time.Millisecond,
			Multiplier:   2.0,
			Jitter:       0,
		},
	}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/retry-after", nil)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", calls.Load())
	}

	// Should be capped around MaxDelay (~40ms), tolerant bounds: [30ms, 250ms]
	if duration < 30*time.Millisecond || duration > 250*time.Millisecond {
		t.Fatalf("expected duration capped at MaxDelay ~40ms, got %v", duration)
	}
}

type unrewindableReader struct {
	io.Reader
}

func TestRetryTransport_BodySafety(t *testing.T) {
	t.Run("body with GetBody allows retries", func(t *testing.T) {
		var calls atomic.Int32
		stub := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			body, _ := io.ReadAll(r.Body)
			if string(body) != "payload" {
				t.Errorf("expected body 'payload', got %q", string(body))
			}
			if calls.Load() < 2 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(bytes.NewBufferString("retry")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("ok")),
				Header:     make(http.Header),
			}, nil
		})

		rt := &RetryTransport{
			Base: stub,
			Policy: RetryPolicy{
				MaxAttempts:  3,
				InitialDelay: time.Millisecond,
				MaxDelay:     10 * time.Millisecond,
				Multiplier:   2.0,
			},
		}

		// http.NewRequest automatically sets GetBody for *bytes.Reader
		req, _ := http.NewRequest(http.MethodPut, "http://example.com/put", bytes.NewReader([]byte("payload")))
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if calls.Load() != 2 {
			t.Fatalf("expected 2 calls with rewound body, got %d", calls.Load())
		}
	})

	t.Run("body without GetBody enforces single attempt", func(t *testing.T) {
		var calls atomic.Int32
		stub := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(bytes.NewBufferString("err")),
				Header:     make(http.Header),
			}, nil
		})

		rt := &RetryTransport{
			Base: stub,
			Policy: RetryPolicy{
				MaxAttempts:  3,
				InitialDelay: time.Millisecond,
				MaxDelay:     10 * time.Millisecond,
				Multiplier:   2.0,
			},
		}

		req, _ := http.NewRequest(http.MethodPut, "http://example.com/put", &unrewindableReader{Reader: bytes.NewReader([]byte("stream"))})
		req.GetBody = nil // enforce nil GetBody
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if calls.Load() != 1 {
			t.Fatalf("expected unrewindable body to make exactly 1 attempt, got %d", calls.Load())
		}
	})
}
