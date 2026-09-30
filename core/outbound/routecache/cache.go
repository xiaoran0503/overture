package routecache

import (
	"container/list"
	"sync"
	"time"

	"github.com/shawn1m/overture/core/metrics"
)

// Cache remembers the domain-table decision for a name (Primary,
// Alternative, or undecided). Later misses skip domain-list scans. IP-network
// classify results are never stored here.
//
// Nil-safe: New(0, _) returns nil and every method is a no-op, matching
// historical behaviour when the feature is left off.
type Cache struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	items map[string]*list.Element
	lru   *list.List
	now   func() time.Time
}

type entry struct {
	key   string
	group string
	exp   time.Time
}

// New returns a bounded TTL LRU. size <= 0 disables the cache (returns nil).
// ttlSeconds <= 0 defaults to 600.
func New(size, ttlSeconds int) *Cache {
	if size <= 0 {
		return nil
	}
	if ttlSeconds <= 0 {
		ttlSeconds = 600
	}
	return &Cache{
		max:   size,
		ttl:   time.Duration(ttlSeconds) * time.Second,
		items: make(map[string]*list.Element, size),
		lru:   list.New(),
		now:   time.Now,
	}
}

// Lookup returns the first live entry among keys. A disabled cache always
// misses and does not increment metrics.
func (c *Cache) Lookup(keys ...string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for _, key := range keys {
		if key == "" {
			continue
		}
		if group, ok := c.getLocked(key, now); ok {
			metrics.RouteCacheHitsTotal.Inc()
			return group, true
		}
	}
	metrics.RouteCacheMissesTotal.Inc()
	return "", false
}

// Put stores group under key, refreshing TTL and LRU position.
func (c *Cache) Put(key, group string) {
	if c == nil || key == "" || group == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if e, ok := c.items[key]; ok {
		ent := e.Value.(*entry)
		ent.group = group
		ent.exp = now.Add(c.ttl)
		c.lru.MoveToFront(e)
		return
	}
	for c.lru.Len() >= c.max {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.items, oldest.Value.(*entry).key)
	}
	e := c.lru.PushFront(&entry{key: key, group: group, exp: now.Add(c.ttl)})
	c.items[key] = e
}

func (c *Cache) getLocked(key string, now time.Time) (string, bool) {
	e, ok := c.items[key]
	if !ok {
		return "", false
	}
	ent := e.Value.(*entry)
	if !ent.exp.After(now) {
		c.lru.Remove(e)
		delete(c.items, key)
		return "", false
	}
	c.lru.MoveToFront(e)
	return ent.group, true
}

// Len is the number of entries, including those not yet expired.
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}
