package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// BenchmarkCacheHit measures the hot-path latency and allocations of serving an HTTP request
// from the in-memory response cache without touching downstream backends or network transports.
func BenchmarkCacheHit(b *testing.B) {
	c := NewCache(1000, 64*1024)
	key := "GET /api/users?"
	c.Put(key, &Entry{
		Status: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body:      []byte(`{"users":[{"id":1,"name":"Alice"},{"id":2,"name":"Bob"}]}`),
		ExpiresAt: time.Now().Add(time.Hour),
		ETag:      `"test-etag-12345"`,
	})

	backend := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		b.Fatal("backend must not be called on cache hit")
	})
	handler := Middleware(c, time.Hour, 64*1024)(backend)

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	rec := httptest.NewRecorder()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		rec.Body.Reset()
		clear(rec.Header())
		handler.ServeHTTP(rec, req)
	}
}
