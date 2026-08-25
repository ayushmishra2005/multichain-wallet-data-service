package api

import (
	"maps"
	"sync"
	"time"
)

type cacheEntry struct {
	val    Summary
	expiry time.Time
}

// Cache stores successful summary responses for a short TTL.
type Cache struct {
	mu    sync.Mutex
	items map[string]cacheEntry
	max   int
	ttl   time.Duration
	now   func() time.Time
}

func NewCache(max int, ttl time.Duration) *Cache {
	if max <= 0 {
		max = 1024
	}
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &Cache{
		items: make(map[string]cacheEntry),
		max:   max,
		ttl:   ttl,
		now:   time.Now,
	}
}

func (c *Cache) Get(key string) (Summary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok {
		return Summary{}, false
	}
	if !c.now().Before(e.expiry) {
		delete(c.items, key)
		return Summary{}, false
	}
	return cloneSummary(e.val), true
}

func (c *Cache) Set(key string, val Summary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, e := range c.items {
		if !now.Before(e.expiry) {
			delete(c.items, k)
		}
	}
	if _, exists := c.items[key]; !exists && len(c.items) >= c.max {
		c.evictOldest()
	}
	c.items[key] = cacheEntry{val: cloneSummary(val), expiry: now.Add(c.ttl)}
}

func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

func (c *Cache) evictOldest() {
	var (
		oldestKey string
		oldest    time.Time
		first     = true
	)
	for k, e := range c.items {
		if first || e.expiry.Before(oldest) {
			oldestKey = k
			oldest = e.expiry
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.items, oldestKey)
	}
}

func cloneSummary(s Summary) Summary {
	out := s
	out.ByType = maps.Clone(s.ByType)
	out.ByChain = maps.Clone(s.ByChain)
	return out
}
