package rag

import (
	"testing"
	"time"
)

// --- ttlCache tests ---

func TestTTLCache_BasicStoreLoad(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	c.Store("key1", "value1")

	v, ok := c.Load("key1")
	if !ok {
		t.Fatal("expected key1 to be found")
	}
	if v.(string) != "value1" {
		t.Errorf("expected 'value1', got %q", v)
	}
}

func TestTTLCache_MissReturnsNotFound(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	_, ok := c.Load("missing")
	if ok {
		t.Error("expected missing key to not be found")
	}
}

func TestTTLCache_ExpiredEntryEvicted(t *testing.T) {
	c := newTTLCache(1*time.Millisecond, 10)
	c.Store("key1", "value1")
	time.Sleep(5 * time.Millisecond)

	_, ok := c.Load("key1")
	if ok {
		t.Error("expected expired entry to be evicted on Load")
	}
}

func TestTTLCache_MaxSizeEvictsOldest(t *testing.T) {
	c := newTTLCache(1*time.Hour, 3) // small cache
	c.Store("a", "1")
	c.Store("b", "2")
	c.Store("c", "3")
	// At capacity — this should evict "a" (oldest)
	c.Store("d", "4")

	if _, ok := c.Load("a"); ok {
		t.Error("expected 'a' to be evicted (oldest)")
	}
	if _, ok := c.Load("d"); !ok {
		t.Error("expected 'd' to be present")
	}
}

func TestTTLCache_OverwriteExistingKey(t *testing.T) {
	c := newTTLCache(1*time.Minute, 10)
	c.Store("key", "v1")
	c.Store("key", "v2")

	v, ok := c.Load("key")
	if !ok {
		t.Fatal("expected key to be found")
	}
	if v.(string) != "v2" {
		t.Errorf("expected 'v2', got %q", v)
	}
}
