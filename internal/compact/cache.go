package compact

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

/* defaultSummaryCacheMax bounds the cache so a long-lived server cannot grow it without limit. */
const defaultSummaryCacheMax = 256

/*
SummaryCache memoizes summaries by content hash so an identical oversized message
— e.g. a large paste the web UI replays verbatim on every turn of a conversation
— is summarized once instead of once per turn. It is safe for concurrent use and
is shared by pointer across the per-request Compactor copies, so the memo persists
across requests. Eviction is FIFO once max entries is reached.
*/
type SummaryCache struct {
	mu    sync.Mutex
	m     map[string]string
	order []string
	max   int
}

/* NewSummaryCache returns a cache holding at most max entries (<=0 uses the default). */
func NewSummaryCache(max int) *SummaryCache {
	if max <= 0 {
		max = defaultSummaryCacheMax
	}
	return &SummaryCache{m: make(map[string]string, max), max: max}
}

/* hashContent is the cache key: a hex SHA-256 of the exact message content. */
func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *SummaryCache) get(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[key]
	return v, ok
}

func (c *SummaryCache) put(key, val string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		c.m[key] = val
		return
	}
	if len(c.order) >= c.max {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.m, oldest)
	}
	c.m[key] = val
	c.order = append(c.order, key)
}
