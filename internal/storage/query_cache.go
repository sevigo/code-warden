package storage

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/sevigo/goframe/schema"
)

type cacheEntry struct {
	docs      []schema.Document
	expiresAt time.Time
}

type queryCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
	maxSize int
}

func newQueryCache(ttl time.Duration, maxSize int) *queryCache {
	return &queryCache{
		entries: make(map[string]cacheEntry, maxSize),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (c *queryCache) key(collection, query string, numDocs int) string {
	h := sha256.Sum256([]byte(query))
	return fmt.Sprintf("%s|%x|%d", collection, h[:8], numDocs)
}

func (c *queryCache) get(collection, query string, numDocs int) ([]schema.Document, bool) {
	k := c.key(collection, query, numDocs)
	c.mu.RLock()
	entry, ok := c.entries[k]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	out := make([]schema.Document, len(entry.docs))
	copy(out, entry.docs)
	return out, true
}

func (c *queryCache) set(collection, query string, numDocs int, docs []schema.Document) {
	if len(docs) == 0 {
		return
	}
	k := c.key(collection, query, numDocs)
	now := time.Now()

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[k]; !exists && len(c.entries) >= c.maxSize {
		var oldestKey string
		oldestAt := time.Time{}
		for ek, e := range c.entries {
			if e.expiresAt.Before(oldestAt) || oldestAt.IsZero() {
				oldestKey = ek
				oldestAt = e.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}

	cp := make([]schema.Document, len(docs))
	copy(cp, docs)
	c.entries[k] = cacheEntry{docs: cp, expiresAt: now.Add(c.ttl)}
}

func (c *queryCache) invalidate(collection string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) >= len(collection) && k[:len(collection)] == collection {
			delete(c.entries, k)
		}
	}
}

func (c *queryCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry, c.maxSize)
}
