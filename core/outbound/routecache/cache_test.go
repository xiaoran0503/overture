package routecache

import (
	"testing"
	"time"
)

func TestNilCacheIsNoop(t *testing.T) {
	var c *Cache
	if c.Len() != 0 {
		t.Fatal("nil Len")
	}
	c.Put("d|example.com", "Primary")
	if _, ok := c.Lookup("d|example.com"); ok {
		t.Fatal("nil Lookup hit")
	}
}

func TestNewZeroSizeDisabled(t *testing.T) {
	if New(0, 600) != nil || New(-1, 600) != nil {
		t.Fatal("size <= 0 must return nil")
	}
}

func TestPutGetAndLRURefresh(t *testing.T) {
	c := New(2, 60)
	c.Put("a", "Primary")
	c.Put("b", "Alternative")
	if g, ok := c.Lookup("a"); !ok || g != "Primary" {
		t.Fatalf("a = %q %v", g, ok)
	}
	c.Put("c", "Primary") // evict LRU: b, because a was looked up
	if _, ok := c.Lookup("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if g, ok := c.Lookup("c"); !ok || g != "Primary" {
		t.Fatalf("c = %q %v", g, ok)
	}
	if c.Len() != 2 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestTTLExpiry(t *testing.T) {
	c := New(8, 10)
	now := time.Unix(1_000, 0)
	c.now = func() time.Time { return now }
	c.Put("a", "Primary")
	if _, ok := c.Lookup("a"); !ok {
		t.Fatal("expected hit before expiry")
	}
	now = now.Add(10 * time.Second)
	if _, ok := c.Lookup("a"); ok {
		t.Fatal("expected miss after ttl")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry should be removed, len=%d", c.Len())
	}
}

func TestLookupFirstLiveKey(t *testing.T) {
	c := New(8, 60)
	c.Put("d|example.com", "Alternative")
	g, ok := c.Lookup("d|example.com", "i|example.com|1||")
	if !ok || g != "Alternative" {
		t.Fatalf("got %q %v", g, ok)
	}
}

func TestDefaultTTLWhenZero(t *testing.T) {
	c := New(1, 0)
	if c == nil || c.ttl != 600*time.Second {
		t.Fatalf("ttl = %v", c.ttl)
	}
}

func TestPutIgnoresEmpty(t *testing.T) {
	c := New(4, 60)
	c.Put("", "Primary")
	c.Put("a", "")
	if c.Len() != 0 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestPutOverwritesAndMovesToFront(t *testing.T) {
	c := New(1, 60)
	c.Put("a", "Primary")
	c.Put("a", "Alternative")
	if g, ok := c.Lookup("a"); !ok || g != "Alternative" {
		t.Fatalf("got %q %v", g, ok)
	}
	if c.Len() != 1 {
		t.Fatalf("len = %d", c.Len())
	}
}
