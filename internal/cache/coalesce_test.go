package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// missStore wraps a Store, counts lookups, and always reports a miss. Tests use it to
// wait until every concurrent request has passed the cache check, and to guarantee that
// late arrivals cannot be served from the cache instead of being coalesced.
type missStore struct {
	inner Store
	gets  atomic.Int32
}

func (s *missStore) Get(string) (*Entry, bool) {
	s.gets.Add(1)
	return nil, false
}

func (s *missStore) Put(key string, e *Entry) { s.inner.Put(key, e) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}

// blockingBackend counts invocations and blocks every call until release is closed.
func blockingBackend(calls *atomic.Int32, release <-chan struct{}, status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// fireConcurrent sends n concurrent GETs for path and releases the backend once all of
// them have passed the cache lookup and had a moment to join the in-flight call.
func fireConcurrent(t *testing.T, handler http.Handler, store *missStore, release chan struct{}, n int, path string) []*httptest.ResponseRecorder {
	t.Helper()

	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		recs[i] = httptest.NewRecorder()
		wg.Add(1)
		go func(rec *httptest.ResponseRecorder) {
			defer wg.Done()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		}(recs[i])
	}

	waitFor(t, func() bool { return store.gets.Load() == int32(n) })
	time.Sleep(20 * time.Millisecond) // let the last lookups reach singleflight
	close(release)
	wg.Wait()

	return recs
}

func TestCacheMiddleware_CoalescesConcurrentMisses(t *testing.T) {
	const n = 50
	store := &missStore{inner: NewCache(10, 1024)}
	var calls atomic.Int32
	release := make(chan struct{})

	handler := CacheMiddleware(store, time.Minute, 1024)(blockingBackend(&calls, release, http.StatusOK, "shared-response"))
	recs := fireConcurrent(t, handler, store, release, n, "/stampede")

	if got := calls.Load(); got != 1 {
		t.Fatalf("backend calls = %d, want exactly 1", got)
	}
	for i, rec := range recs {
		if rec.Code != http.StatusOK {
			t.Fatalf("caller %d status = %d, want 200", i, rec.Code)
		}
		if rec.Body.String() != "shared-response" {
			t.Fatalf("caller %d body = %q, want full shared response", i, rec.Body.String())
		}
		if rec.Header().Get("X-Cache") != "MISS" {
			t.Fatalf("caller %d X-Cache = %q, want MISS", i, rec.Header().Get("X-Cache"))
		}
		if rec.Header().Get("ETag") != `"v1"` {
			t.Fatalf("caller %d ETag = %q, want \"v1\"", i, rec.Header().Get("ETag"))
		}
	}
}

func TestCacheMiddleware_CoalescingDoesNotShareFailures(t *testing.T) {
	const n = 20
	store := &missStore{inner: NewCache(10, 1024)}
	var calls atomic.Int32
	release := make(chan struct{})

	handler := CacheMiddleware(store, time.Minute, 1024)(blockingBackend(&calls, release, http.StatusInternalServerError, "boom"))
	recs := fireConcurrent(t, handler, store, release, n, "/failing")

	if got := calls.Load(); got != n {
		t.Fatalf("backend calls = %d, want %d (each caller observes failure independently)", got, n)
	}
	for i, rec := range recs {
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("caller %d status = %d, want 500", i, rec.Code)
		}
		if rec.Body.String() != "boom" {
			t.Fatalf("caller %d body = %q, want boom", i, rec.Body.String())
		}
	}
}

func TestCacheMiddleware_CoalescingDoesNotShareOversizedBodies(t *testing.T) {
	const n = 20
	store := &missStore{inner: NewCache(10, 10)}
	var calls atomic.Int32
	release := make(chan struct{})
	payload := strings.Repeat("x", 64)

	handler := CacheMiddleware(store, time.Minute, 10)(blockingBackend(&calls, release, http.StatusOK, payload))
	recs := fireConcurrent(t, handler, store, release, n, "/large")

	if got := calls.Load(); got != n {
		t.Fatalf("backend calls = %d, want %d (oversized responses are not shared)", got, n)
	}
	for i, rec := range recs {
		if rec.Body.String() != payload {
			t.Fatalf("caller %d body length = %d, want full %d-byte payload", i, rec.Body.Len(), len(payload))
		}
	}
}

func TestCacheMiddleware_CoalescingIsPerKey(t *testing.T) {
	const n = 10
	var calls atomic.Int32
	handler := CacheMiddleware(NewCache(100, 1024), time.Minute, 1024)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, fmt.Sprintf("/item/%d", i), nil))
		}(i)
	}
	wg.Wait()

	if got := calls.Load(); got != n {
		t.Fatalf("backend calls = %d, want %d (distinct keys are not coalesced)", got, n)
	}
}

func TestCacheMiddleware_CoalescedFollowerHonorsIfNoneMatch(t *testing.T) {
	store := &missStore{inner: NewCache(10, 1024)}
	var calls atomic.Int32
	release := make(chan struct{})
	handler := CacheMiddleware(store, time.Minute, 1024)(blockingBackend(&calls, release, http.StatusOK, "body"))

	leaderRec := httptest.NewRecorder()
	followerRec := httptest.NewRecorder()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(leaderRec, httptest.NewRequest(http.MethodGet, "/etag", nil))
	}()
	waitFor(t, func() bool { return calls.Load() == 1 })
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/etag", nil)
		req.Header.Set("If-None-Match", `"v1"`)
		handler.ServeHTTP(followerRec, req)
	}()
	waitFor(t, func() bool { return store.gets.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("backend calls = %d, want 1", got)
	}
	if leaderRec.Code != http.StatusOK {
		t.Fatalf("leader status = %d, want 200", leaderRec.Code)
	}
	if followerRec.Code != http.StatusNotModified {
		t.Fatalf("follower status = %d, want 304", followerRec.Code)
	}
	if followerRec.Body.Len() != 0 {
		t.Fatalf("follower 304 body = %q, want empty", followerRec.Body.String())
	}
}

func TestCacheMiddleware_NoStoreRequestSkipsCoalescing(t *testing.T) {
	store := &missStore{inner: NewCache(10, 1024)}
	var calls atomic.Int32
	release := make(chan struct{})
	handler := CacheMiddleware(store, time.Minute, 1024)(blockingBackend(&calls, release, http.StatusOK, "body"))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/fresh", nil))
	}()
	waitFor(t, func() bool { return calls.Load() == 1 })
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodGet, "/fresh", nil)
		req.Header.Set("Cache-Control", "no-store")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}()

	// The no-store request must reach the backend while the leader is still in flight.
	waitFor(t, func() bool { return calls.Load() == 2 })
	close(release)
	wg.Wait()
}

func TestCacheMiddleware_LeaderPanicNotSharedWithFollowers(t *testing.T) {
	const n = 10
	store := &missStore{inner: NewCache(10, 1024)}
	var calls atomic.Int32
	release := make(chan struct{})

	handler := CacheMiddleware(store, time.Minute, 1024)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			<-release
			panic(http.ErrAbortHandler)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	var panics atomic.Int32
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		recs[i] = httptest.NewRecorder()
		wg.Add(1)
		go func(rec *httptest.ResponseRecorder) {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					panics.Add(1)
				}
			}()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
		}(recs[i])
	}
	waitFor(t, func() bool { return store.gets.Load() == n })
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := panics.Load(); got != 1 {
		t.Fatalf("panics = %d, want 1 (only the leader)", got)
	}
	ok := 0
	for _, rec := range recs {
		if rec.Code == http.StatusOK && rec.Body.String() == "ok" {
			ok++
		}
	}
	if ok != n-1 {
		t.Fatalf("successful followers = %d, want %d", ok, n-1)
	}
}
