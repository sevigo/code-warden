package storage

import (
	"testing"
	"time"

	"github.com/sevigo/goframe/schema"
)

func TestQueryCacheSetGet(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	docs := []schema.Document{
		{PageContent: "hello"},
		{PageContent: "world"},
	}

	c.set("col1", "query1", 5, docs)

	got, ok := c.get("col1", "query1", 5)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(got))
	}
	if got[0].PageContent != "hello" {
		t.Errorf("expected 'hello', got %q", got[0].PageContent)
	}
}

func TestQueryCacheMiss(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)

	_, ok := c.get("col1", "nonexistent", 5)
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestQueryCacheExpiration(t *testing.T) {
	c := newQueryCache(1*time.Nanosecond, 100)
	docs := []schema.Document{{PageContent: "stale"}}

	c.set("col1", "q", 1, docs)
	time.Sleep(10 * time.Millisecond)

	_, ok := c.get("col1", "q", 1)
	if ok {
		t.Fatal("expected expired entry to miss")
	}
}

func TestQueryCacheDifferentCollection(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	docs := []schema.Document{{PageContent: "data"}}

	c.set("col1", "query", 5, docs)

	_, ok := c.get("col2", "query", 5)
	if ok {
		t.Fatal("expected miss for different collection")
	}
}

func TestQueryCacheIsolation(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	docs := []schema.Document{{PageContent: "original"}}
	mutated := []schema.Document{{PageContent: "changed"}}

	c.set("col1", "q", 1, docs)

	got, _ := c.get("col1", "q", 1)
	got[0].PageContent = "changed"

	got2, _ := c.get("col1", "q", 1)
	if got2[0].PageContent == "changed" {
		t.Fatal("cache entry mutated through returned slice")
	}
	_ = mutated
}

func TestQueryCacheInvalidate(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	c.set("col1", "q1", 5, []schema.Document{{PageContent: "a"}})
	c.set("col1", "q2", 5, []schema.Document{{PageContent: "b"}})
	c.set("col2", "q1", 5, []schema.Document{{PageContent: "c"}})

	c.invalidate("col1")

	if _, ok := c.get("col1", "q1", 5); ok {
		t.Fatal("col1/q1 should be invalidated")
	}
	if _, ok := c.get("col1", "q2", 5); ok {
		t.Fatal("col1/q2 should be invalidated")
	}
	if _, ok := c.get("col2", "q1", 5); !ok {
		t.Fatal("col2/q1 should still exist")
	}
}

func TestQueryCacheInvalidate_NoPrefixCollision(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	c.set("myrepo", "q1", 5, []schema.Document{{PageContent: "a"}})
	c.set("myrepo-extra", "q1", 5, []schema.Document{{PageContent: "b"}})

	c.invalidate("myrepo")

	if _, ok := c.get("myrepo", "q1", 5); ok {
		t.Fatal("myrepo/q1 should be invalidated")
	}
	if _, ok := c.get("myrepo-extra", "q1", 5); !ok {
		t.Fatal("myrepo-extra/q1 should still exist — prefix collision bug")
	}
}

func TestQueryCacheClear(t *testing.T) {
	c := newQueryCache(5*time.Minute, 100)
	c.set("col1", "q1", 5, []schema.Document{{PageContent: "a"}})
	c.set("col2", "q2", 5, []schema.Document{{PageContent: "b"}})

	c.clear()

	if _, ok := c.get("col1", "q1", 5); ok {
		t.Fatal("expected miss after clear")
	}
	if _, ok := c.get("col2", "q2", 5); ok {
		t.Fatal("expected miss after clear")
	}
}

func TestQueryCacheEviction(t *testing.T) {
	c := newQueryCache(5*time.Minute, 3)
	for i := range 5 {
		c.set("col", string(rune('a'+i)), 1, []schema.Document{{PageContent: string(rune('a' + i))}})
	}

	c.mu.RLock()
	count := len(c.entries)
	c.mu.RUnlock()

	if count > 3 {
		t.Fatalf("expected at most 3 entries, got %d", count)
	}
}

func TestQueryCacheOverwriteNoEviction(t *testing.T) {
	c := newQueryCache(5*time.Minute, 3)
	docs := []schema.Document{{PageContent: "v1"}}
	c.set("col", "q1", 1, docs)
	c.set("col", "q2", 1, docs)
	c.set("col", "q3", 1, docs)

	if len(c.entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(c.entries))
	}

	c.set("col", "q1", 1, []schema.Document{{PageContent: "v2"}})

	c.mu.RLock()
	count := len(c.entries)
	c.mu.RUnlock()

	if count != 3 {
		t.Fatalf("overwrite should not trigger eviction, expected 3 entries, got %d", count)
	}

	got, ok := c.get("col", "q1", 1)
	if !ok || got[0].PageContent != "v2" {
		t.Fatal("overwritten entry should have new value")
	}
}
