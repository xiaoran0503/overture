// Copyright (c) 2014 The SkyDNS Authors. All rights reserved.
// Use of this source code is governed by the MIT license.

// Package cache implements DNS caching with EDNS client-subnet support.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/redis/go-redis/v9"
	log "github.com/sirupsen/logrus"
)

const redisOperationTimeout = 2 * time.Second

type elem struct {
	expiration time.Time
	storedAt   time.Time
	msg        *dns.Msg
}

type elemData struct {
	Expiration time.Time
	StoredAt   time.Time
	Msg        []byte
}

func (e *elem) MarshalBinary() ([]byte, error) {
	msgBytes, err := e.msg.Pack()
	if err != nil {
		return nil, err
	}
	return json.Marshal(elemData{Expiration: e.expiration, StoredAt: e.storedAt, Msg: msgBytes})
}

func (e *elem) UnmarshalBinary(data []byte) error {
	var dataValue elemData
	if err := json.Unmarshal(data, &dataValue); err != nil {
		return err
	}
	e.expiration = dataValue.Expiration
	e.storedAt = dataValue.StoredAt
	e.msg = &dns.Msg{}
	return e.msg.Unpack(dataValue.Msg)
}

// Cache holds local DNS responses or delegates storage to Redis.
type Cache struct {
	sync.RWMutex

	capacity    int
	table       map[string]*elem
	redisClient *redis.Client
}

func New(capacity int, redisURL string, redisPoolSize int) *Cache {
	if capacity <= 0 {
		return nil
	}
	c := &Cache{capacity: capacity, table: make(map[string]*elem)}
	if redisURL == "" {
		return c
	}

	options, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Errorf("invalid cacheRedisUrl: %s", err)
		return c
	}
	if redisPoolSize > 0 {
		options.PoolSize = redisPoolSize
	}
	c.redisClient = redis.NewClient(options)
	log.Infof("Redis cache configured at %s", options.Addr)
	return c
}

func (c *Cache) Capacity() int {
	if c == nil {
		return 0
	}
	return c.capacity
}

// Close releases the Redis client's connection pool.
func (c *Cache) Close() error {
	if c == nil || c.redisClient == nil {
		return nil
	}
	return c.redisClient.Close()
}

func (c *Cache) Remove(key string) {
	if c == nil {
		return
	}
	if c.redisClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
		defer cancel()
		if err := c.redisClient.Del(ctx, key).Err(); err != nil && err != redis.Nil {
			log.Warnf("Redis delete for cache failed: %s", err)
		}
		return
	}
	c.Lock()
	delete(c.table, key)
	c.Unlock()
}

// EvictRandom removes enough arbitrary members to retain capacity. Caller holds c.Lock.
func (c *Cache) EvictRandom() {
	over := len(c.table) - c.capacity
	for key := range c.table {
		if over <= 0 {
			return
		}
		delete(c.table, key)
		over--
	}
}

func (c *Cache) InsertMessage(key string, message *dns.Msg, fallbackTTL uint32) {
	if c == nil || c.capacity <= 0 || message == nil {
		return
	}
	if c.redisClient == nil {
		c.InsertMessageToLocal(key, message, fallbackTTL)
		return
	}
	if err := c.InsertMessageToRedis(key, message, fallbackTTL); err != nil {
		log.Warnf("insert cache failed for %s: %s", key, err)
	}
}

func (c *Cache) InsertMessageToRedis(key string, message *dns.Msg, fallbackTTL uint32) error {
	ttl := cacheTTL(message, fallbackTTL)
	if ttl <= 0 {
		return nil
	}
	now := time.Now()
	value := &elem{expiration: now.Add(ttl), storedAt: now, msg: message.Copy()}
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()
	return c.redisClient.Set(ctx, key, value, ttl).Err()
}

func (c *Cache) InsertMessageToLocal(key string, message *dns.Msg, fallbackTTL uint32) {
	ttl := cacheTTL(message, fallbackTTL)
	if ttl <= 0 {
		return
	}
	c.Lock()
	defer c.Unlock()
	if _, exists := c.table[key]; !exists {
		now := time.Now()
		c.table[key] = &elem{expiration: now.Add(ttl), storedAt: now, msg: message.Copy()}
	}
	c.EvictRandom()
}

func cacheTTL(message *dns.Msg, fallbackTTL uint32) time.Duration {
	ttl := fallbackTTL
	found := false
	for _, section := range [][]dns.RR{message.Answer, message.Ns, message.Extra} {
		for _, record := range section {
			if record.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if !found || record.Header().Ttl < ttl {
				ttl = record.Header().Ttl
				found = true
			}
		}
	}
	return time.Duration(ttl) * time.Second
}

// Search returns a copy of a cached message and its expiration timestamp.
func (c *Cache) Search(key string) (*dns.Msg, time.Time, bool) {
	entry, ok := c.get(key)
	if !ok {
		return nil, time.Time{}, false
	}
	return entry.msg, entry.expiration, true
}

func (c *Cache) SearchFromRedis(key string) (*dns.Msg, time.Time, bool) {
	entry, ok := c.getFromRedis(key)
	if !ok {
		return nil, time.Time{}, false
	}
	return entry.msg, entry.expiration, true
}

func (c *Cache) SearchFromLocal(key string) (*dns.Msg, time.Time, bool) {
	entry, ok := c.getFromLocal(key)
	if !ok {
		return nil, time.Time{}, false
	}
	return entry.msg, entry.expiration, true
}

func (c *Cache) get(key string) (*elem, bool) {
	if c == nil || c.capacity <= 0 {
		return nil, false
	}
	if c.redisClient != nil {
		return c.getFromRedis(key)
	}
	return c.getFromLocal(key)
}

func (c *Cache) getFromRedis(key string) (*elem, bool) {
	var value elem
	ctx, cancel := context.WithTimeout(context.Background(), redisOperationTimeout)
	defer cancel()
	if err := c.redisClient.Get(ctx, key).Scan(&value); err != nil {
		if err != redis.Nil {
			log.Warnf("Redis get for cache failed: %s", err)
		}
		return nil, false
	}
	return &value, true
}

func (c *Cache) getFromLocal(key string) (*elem, bool) {
	c.RLock()
	defer c.RUnlock()
	value, ok := c.table[key]
	if !ok || value.msg == nil {
		return nil, false
	}
	return &elem{expiration: value.expiration, storedAt: value.storedAt, msg: value.msg.Copy()}, true
}

func Key(question dns.Question, ednsIP string) string {
	// DNS names are case-insensitive, so cache keys are normalized to lower
	// case to share entries between "WWW.EXAMPLE.COM" and "www.example.com".
	return fmt.Sprintf("%s %d %s", strings.ToLower(question.Name), question.Qtype, ednsIP)
}

// Hit returns a correctly aged message, deleting expired local or Redis entries.
// The response's question section is rewritten to the current query so a
// shared, case-normalized cache entry still echoes the exact query text back.
func (c *Cache) Hit(key string, question dns.Question, messageID uint16) *dns.Msg {
	entry, ok := c.get(key)
	if !ok {
		return nil
	}
	if time.Until(entry.expiration) <= 0 {
		c.Remove(key)
		return nil
	}
	// Defend against corrupt entries (e.g. incompatible Redis payloads
	// written by another program or an older schema) instead of panicking.
	if entry.msg == nil {
		c.Remove(key)
		return nil
	}
	entry.msg.Id = messageID
	entry.msg.Question = []dns.Question{question}
	entry.msg.Compress = true
	entry.msg.Truncated = false
	if !entry.storedAt.IsZero() {
		decrementTTLs(entry.msg, time.Since(entry.storedAt))
	}
	return entry.msg
}

func decrementTTLs(message *dns.Msg, elapsed time.Duration) {
	if elapsed <= 0 {
		return
	}
	seconds := uint32(elapsed / time.Second)
	for _, section := range [][]dns.RR{message.Answer, message.Ns, message.Extra} {
		for _, record := range section {
			if record.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if record.Header().Ttl > seconds {
				record.Header().Ttl -= seconds
			} else {
				record.Header().Ttl = 0
			}
		}
	}
}

// Dump returns local cache contents for diagnostics. Redis does not support enumeration here.
func (c *Cache) Dump(nobody bool) (map[string][]string, int) {
	if c == nil || c.capacity <= 0 {
		return nil, 0
	}
	c.RLock()
	defer c.RUnlock()
	entries := make(map[string][]string)
	if nobody {
		return entries, len(c.table)
	}
	for key, value := range c.table {
		for _, answer := range value.msg.Answer {
			entries[key] = append(entries[key], answer.String())
		}
	}
	return entries, len(c.table)
}
