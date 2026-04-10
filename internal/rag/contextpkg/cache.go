package contextpkg

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sevigo/goframe/schema"

	internalgithub "github.com/sevigo/code-warden/internal/github"
)

type contextCacheEntry struct {
	result    *ContextResult
	expiresAt time.Time
}

type ContextCache struct {
	mu      sync.RWMutex
	entries map[string]contextCacheEntry
	ttl     time.Duration
	maxSize int
}

func NewContextCache(ttl time.Duration, maxSize int) *ContextCache {
	return &ContextCache{
		entries: make(map[string]contextCacheEntry, maxSize),
		ttl:     ttl,
		maxSize: maxSize,
	}
}

func (c *ContextCache) cacheKey(collection, embedderModel, repoPath, prDescription string, changedFiles []internalgithub.ChangedFile) string {
	h := sha256.New()
	h.Write([]byte(collection))
	h.Write([]byte(embedderModel))
	h.Write([]byte(repoPath))
	h.Write([]byte(prDescription))
	for _, f := range changedFiles {
		h.Write([]byte(f.Filename))
		h.Write([]byte(f.Patch))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (c *ContextCache) Get(key string) (*ContextResult, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.result, true
}

func (c *ContextCache) Set(key string, result *ContextResult) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxSize {
		var oldestKey string
		oldestAt := time.Time{}
		for k, e := range c.entries {
			if e.expiresAt.Before(oldestAt) || oldestAt.IsZero() {
				oldestKey = k
				oldestAt = e.expiresAt
			}
		}
		delete(c.entries, oldestKey)
	}

	c.entries[key] = contextCacheEntry{
		result:    result,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *ContextCache) Marshal() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snapshot := make(map[string]contextCacheEntry, len(c.entries))
	for k, v := range c.entries {
		snapshot[k] = v
	}
	return json.Marshal(snapshot)
}

type cachingBuilder struct {
	inner Builder
	cache *ContextCache
}

func NewCachingBuilder(inner Builder, cache *ContextCache) Builder {
	return &cachingBuilder{inner: inner, cache: cache}
}

func (b *cachingBuilder) BuildRelevantContextWithImpact(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) *ContextResult {
	key := b.cache.cacheKey(collectionName, embedderModelName, repoPath, prDescription, changedFiles)
	if result, ok := b.cache.Get(key); ok {
		return result
	}

	result := b.inner.BuildRelevantContextWithImpact(ctx, collectionName, embedderModelName, repoPath, changedFiles, prDescription)
	b.cache.Set(key, result)
	return result
}

func (b *cachingBuilder) BuildRelevantContext(ctx context.Context, collectionName, embedderModelName, repoPath string, changedFiles []internalgithub.ChangedFile, prDescription string) (string, string) {
	result := b.BuildRelevantContextWithImpact(ctx, collectionName, embedderModelName, repoPath, changedFiles, prDescription)
	return result.FullContext, result.DefinitionsContext
}

func (b *cachingBuilder) BuildContextForPrompt(docs []schema.Document) string {
	return b.inner.BuildContextForPrompt(docs)
}

func (b *cachingBuilder) GenerateArchSummaries(ctx context.Context, collectionName, embedderModelName, repoPath string, targetPaths []string) error {
	return b.inner.GenerateArchSummaries(ctx, collectionName, embedderModelName, repoPath, targetPaths)
}

func (b *cachingBuilder) GenerateComparisonSummaries(ctx context.Context, models []string, repoPath string, relPaths []string) (map[string]map[string]string, error) {
	return b.inner.GenerateComparisonSummaries(ctx, models, repoPath, relPaths)
}

func (b *cachingBuilder) GenerateProjectContext(ctx context.Context, collectionName, embedderModelName string) (string, error) {
	return b.inner.GenerateProjectContext(ctx, collectionName, embedderModelName)
}

func (b *cachingBuilder) GeneratePackageSummaries(ctx context.Context, collectionName, embedderModelName string) error {
	return b.inner.GeneratePackageSummaries(ctx, collectionName, embedderModelName)
}
