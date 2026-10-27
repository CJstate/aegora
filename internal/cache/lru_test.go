package cache

import (
	"net/http"
	"sync"
	"testing"
	"time"
)

// newTestEntry builds a minimally valid cache entry that expires in one minute.
func newTestEntry() *Entry {
	return &Entry{
		Status:    http.StatusOK,
		Header:    make(http.Header),
		ExpiresAt: time.Now().Add(time.Minute),
	}
}

// hasKey reports whether key is currently indexed by the cache.
func hasKey(c *Cache, key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.entries[key]
	return ok
}

// TestCache_LRUEviction_ReadProtectsKey covers the acceptance criteria of the LRU
// eviction issue: fill the cache to capacity, read a subset of the keys, then insert
// new keys and assert that the keys never read were evicted while the recently read
// ones survived.
func TestCache_LRUEviction_ReadProtectsKey(t *testing.T) {
	c := NewCache(4, 1024)

	for _, key := range []string{"a", "b", "c", "d"} {
		c.Put(key, newTestEntry())
	}

	// Read a subset, oldest first, so recency order becomes c < d < a < b.
	for _, key := range []string{"a", "b"} {
		if _, hit := c.Get(key); !hit {
			t.Fatalf("expected %q to be a cache hit before eviction", key)
		}
	}

	// Two inserts at capacity must evict exactly the two keys that were never read.
	c.Put("e", newTestEntry())
	c.Put("f", newTestEntry())

	if got := c.order.Len(); got != 4 {
		t.Fatalf("recency list length = %d, want 4", got)
	}
	if len(c.entries) != 4 {
		t.Fatalf("indexed entries = %d, want 4", len(c.entries))
	}

	for _, key := range []string{"c", "d"} {
		if hasKey(c, key) {
			t.Errorf("expected untouched key %q to be evicted", key)
		}
	}
	for _, key := range []string{"a", "b", "e", "f"} {
		if !hasKey(c, key) {
			t.Errorf("expected recently used key %q to survive", key)
		}
	}
}

// TestCache_LRUEviction_WritesRefreshRecency verifies that Put on an existing key
// counts as a use, matching the issue requirement that recency is tracked on both
// reads and writes.
func TestCache_LRUEviction_WritesRefreshRecency(t *testing.T) {
	c := NewCache(2, 1024)

	c.Put("a", newTestEntry())
	c.Put("b", newTestEntry())

	// Overwriting "a" makes it the most recently used, so "b" becomes the victim.
	c.Put("a", newTestEntry())
	c.Put("c", newTestEntry())

	if hasKey(c, "b") {
		t.Error("expected key \"b\" (least recently used) to be evicted")
	}
	for _, key := range []string{"a", "c"} {
		if !hasKey(c, key) {
			t.Errorf("expected key %q to survive", key)
		}
	}
}

// TestCache_LRUEviction_OrderIsStable guards the internal invariants the eviction
// policy relies on: the index and the recency list stay the same length, the front
// element is the least recently used key, and eviction always takes from the front.
func TestCache_LRUEviction_OrderIsStable(t *testing.T) {
	c := NewCache(3, 1024)

	for _, key := range []string{"a", "b", "c"} {
		c.Put(key, newTestEntry())
	}

	if _, hit := c.Get("a"); !hit {
		t.Fatal("expected \"a\" to be a cache hit")
	}

	c.mu.RLock()
	if c.order.Len() != len(c.entries) {
		c.mu.RUnlock()
		t.Fatalf("recency list length %d != indexed entries %d", c.order.Len(), len(c.entries))
	}
	least := c.order.Front().Value.(*cacheItem).key
	most := c.order.Back().Value.(*cacheItem).key
	c.mu.RUnlock()

	if least != "b" {
		t.Errorf("least recently used key = %q, want \"b\"", least)
	}
	if most != "a" {
		t.Errorf("most recently used key = %q, want \"a\"", most)
	}

	c.Put("d", newTestEntry())
	if hasKey(c, "b") {
		t.Error("expected \"b\" to be the eviction victim")
	}
}

// TestCache_LRUEviction_InvalidateKeepsListConsistent ensures explicit invalidation
// removes the recency node too, so the list cannot drift out of sync with the index
// and later evict an already-removed key.
func TestCache_LRUEviction_InvalidateKeepsListConsistent(t *testing.T) {
	c := NewCache(2, 1024)

	c.Put("a", newTestEntry())
	c.Put("b", newTestEntry())

	c.Invalidate("a")

	c.mu.RLock()
	listLen := c.order.Len()
	indexLen := len(c.entries)
	c.mu.RUnlock()

	if listLen != 1 || indexLen != 1 {
		t.Fatalf("after Invalidate: list=%d index=%d, want 1 and 1", listLen, indexLen)
	}

	// Room was freed, so inserting "c" must not evict "b".
	c.Put("c", newTestEntry())
	if !hasKey(c, "b") {
		t.Error("expected \"b\" to survive: capacity was freed by Invalidate")
	}

	c.InvalidatePrefix("c")
	c.mu.RLock()
	listLen = c.order.Len()
	indexLen = len(c.entries)
	c.mu.RUnlock()

	if listLen != 1 || indexLen != 1 {
		t.Fatalf("after InvalidatePrefix: list=%d index=%d, want 1 and 1", listLen, indexLen)
	}
}

// TestCache_LRUEviction_ExpiredEntryFreesSlot checks that reading an expired entry
// frees its slot instead of leaving a dead node that would later be counted as a
// live resident and skew eviction.
func TestCache_LRUEviction_ExpiredEntryFreesSlot(t *testing.T) {
	c := NewCache(2, 1024)

	c.Put("stale", &Entry{Status: http.StatusOK, ExpiresAt: time.Now().Add(-time.Second)})
	c.Put("live", newTestEntry())

	if _, hit := c.Get("stale"); hit {
		t.Fatal("expected expired entry to miss")
	}
	if hasKey(c, "stale") {
		t.Fatal("expected expired entry to be removed from the index")
	}

	c.mu.RLock()
	listLen := c.order.Len()
	c.mu.RUnlock()
	if listLen != 1 {
		t.Fatalf("recency list length = %d, want 1 after lazy expiration", listLen)
	}

	// The freed slot is reusable without evicting the live entry.
	c.Put("new", newTestEntry())
	if !hasKey(c, "live") {
		t.Error("expected \"live\" to survive: the expired slot should be reused")
	}
}

// TestCache_LRUEviction_Concurrent hammers reads and writes under the race detector so
// the recency list stays provably consistent under concurrent promotion and eviction.
func TestCache_LRUEviction_Concurrent(t *testing.T) {
	t.Parallel()

	const (
		capacity = 16
		workers  = 8
		rounds   = 200
	)

	c := NewCache(capacity, 4096)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				key := string(rune('a' + (worker+i)%26))
				switch i % 3 {
				case 0:
					c.Put(key, newTestEntry())
				case 1:
					_, _ = c.Get(key)
				default:
					if i%7 == 0 {
						c.Invalidate(key)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	c.mu.RLock()
	listLen := c.order.Len()
	indexLen := len(c.entries)
	c.mu.RUnlock()

	if listLen != indexLen {
		t.Fatalf("recency list length %d != indexed entries %d after concurrent access", listLen, indexLen)
	}
	if indexLen > capacity {
		t.Fatalf("cache holds %d entries, capacity is %d", indexLen, capacity)
	}
}
