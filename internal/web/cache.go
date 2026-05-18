package web

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

/* DefaultCacheTTL is the time a cached web-search result remains valid. */
const DefaultCacheTTL = 5 * time.Minute

/* DefaultCacheCapacity bounds memory usage; oldest-on-write eviction policy. */
const DefaultCacheCapacity = 256

/*
cacheEntry holds the result slice plus a timestamp. We store sanitized
Results only, so callers never re-sanitize on a cache hit.
*/
type cacheEntry struct {
	results []Result
	at      time.Time
}

/*
Cache is an in-memory TTL cache keyed by a normalized hash of (backend, k,
query). It is safe for concurrent use.
*/
type Cache struct {
	mu       sync.RWMutex
	entries  map[string]cacheEntry
	keys     []string
	ttl      time.Duration
	capacity int
}

/* NewCache returns a Cache with the given TTL; pass 0 to use DefaultCacheTTL. */
func NewCache(ttl time.Duration, capacity int) *Cache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	if capacity <= 0 {
		capacity = DefaultCacheCapacity
	}
	return &Cache{
		entries:  make(map[string]cacheEntry, capacity),
		keys:     make([]string, 0, capacity),
		ttl:      ttl,
		capacity: capacity,
	}
}

/* Get returns a cached slice for the given (backend, k, query) tuple, or false. */
func (c *Cache) Get(backend string, k int, query string) ([]Result, bool) {
	key := cacheKey(backend, k, query)
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) > c.ttl {
		return nil, false
	}
	out := make([]Result, len(e.results))
	copy(out, e.results)
	return out, true
}

/* Put stores a sanitized result slice for the given tuple, evicting oldest entry on capacity. */
func (c *Cache) Put(backend string, k int, query string, results []Result) {
	key := cacheKey(backend, k, query)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		if len(c.keys) >= c.capacity {
			oldest := c.keys[0]
			c.keys = c.keys[1:]
			delete(c.entries, oldest)
		}
		c.keys = append(c.keys, key)
	}
	stored := make([]Result, len(results))
	copy(stored, results)
	c.entries[key] = cacheEntry{results: stored, at: time.Now()}
}

/*
cacheKey normalizes the query (lowercase, trim, collapse whitespace) and
hashes (backend || k || query) so different backends or different result
counts never share a cache slot.
*/
func cacheKey(backend string, k int, query string) string {
	norm := strings.ToLower(strings.TrimSpace(query))
	norm = whitespaceRunRe.ReplaceAllString(norm, " ")
	h := sha256.New()
	_, _ = h.Write([]byte(backend))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte{byte(k)})
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(norm))
	return hex.EncodeToString(h.Sum(nil))[:24]
}
