package routing

import (
	"sync"
	"time"
)

// SeenCache remembers recently handled packet identifiers so that a packet
// that reaches a node through several paths is processed exactly once.
//
// Without it, flooding on a network containing a cycle never terminates: TTL
// alone bounds each copy, but the number of copies still grows exponentially.
type SeenCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
	ttl     time.Duration
}

// NewSeenCache returns a cache that forgets identifiers after ttl, so a long
// run does not grow without bound and a reused id eventually becomes valid.
func NewSeenCache(ttl time.Duration) *SeenCache {
	return &SeenCache{entries: map[string]time.Time{}, ttl: ttl}
}

// Seen records id and reports whether it had already been recorded.
func (c *SeenCache) Seen(id string) bool {
	if id == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	c.evict(now)

	if _, ok := c.entries[id]; ok {
		return true
	}
	c.entries[id] = now
	return false
}

// evict drops expired identifiers. The caller must hold the lock.
func (c *SeenCache) evict(now time.Time) {
	for id, at := range c.entries {
		if now.Sub(at) > c.ttl {
			delete(c.entries, id)
		}
	}
}

// Len reports how many identifiers are currently remembered.
func (c *SeenCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
