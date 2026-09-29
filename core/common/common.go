// Copyright (c) 2016 shawn1m. All rights reserved.
// Use of this source code is governed by The MIT License (MIT) that can be
// found in the LICENSE file.

// Package common provides common functions.
package common

import (
	"container/list"
	"net"
	"regexp"
	"strings"
	"sync"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

var ReservedIPNetworkList = getReservedIPNetworkList()

// regexCacheEntry pairs a compiled pattern with the LRU list element that
// references it, so lookups can move the entry to the front in O(1).
type regexCacheEntry struct {
	pattern string
	re      *regexp.Regexp // nil value = failed compile
}

// regexCache is a bounded LRU cache of compiled regular expressions.
// Per-query TTL and hosts lookups must not recompile the same pattern on
// every record, and query-derived patterns must not grow the cache without
// bound. Failed compilations are cached as nil so a broken pattern is
// compiled at most once instead of on every query.
type regexCache struct {
	mu      sync.Mutex
	max     int
	entries map[string]*list.Element // pattern -> *regexCacheEntry
	lru     *list.List
}

func newRegexCache(max int) *regexCache {
	return &regexCache{max: max, entries: make(map[string]*list.Element), lru: list.New()}
}

func (c *regexCache) get(pattern string) (*regexp.Regexp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[pattern]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(e)
	return e.Value.(*regexCacheEntry).re, true
}

func (c *regexCache) store(pattern string, re *regexp.Regexp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[pattern]; ok {
		e.Value.(*regexCacheEntry).re = re
		c.lru.MoveToFront(e)
		return
	}
	e := c.lru.PushFront(&regexCacheEntry{pattern: pattern, re: re})
	c.entries[pattern] = e
	for c.max > 0 && len(c.entries) > c.max {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.entries, oldest.Value.(*regexCacheEntry).pattern)
	}
}

// compiledRegexCache bounds memoized pattern compilations (roadmap phase 2: regex cache upper bound).
var compiledRegexCache = newRegexCache(4096)

func IsDomainMatchRule(pattern string, domain string) bool {
	if re, ok := compiledRegexCache.get(pattern); ok {
		return re != nil && re.MatchString(domain)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Warnf("Error matching domain %s with pattern %s: %s", domain, pattern, err)
		compiledRegexCache.store(pattern, nil)
		return false
	}
	compiledRegexCache.store(pattern, re)
	return re.MatchString(domain)
}

func HasAnswer(m *dns.Msg) bool { return m != nil && len(m.Answer) != 0 }

func HasSubDomain(s string, sub string) bool {
	return strings.HasSuffix(sub, "."+s) || s == sub
}

func getReservedIPNetworkList() *IPSet {
	var ipNetList []*net.IPNet
	localCIDR := []string{
		"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10",
		// IPv6 loopback, unspecified, unique-local and link-local ranges must
		// never be forwarded as EDNS Client Subnet source addresses.
		"::1/128", "::/128", "fc00::/7", "fe80::/10",
	}
	for _, c := range localCIDR {
		_, ipNet, err := net.ParseCIDR(c)
		if err != nil {
			break
		}
		ipNetList = append(ipNetList, ipNet)
	}
	return NewIPSet(ipNetList)
}

func FindRecordByType(msg *dns.Msg, t uint16) string {
	if msg == nil {
		return ""
	}
	for _, rr := range msg.Answer {
		if rr.Header().Rrtype == t {
			items := strings.SplitN(rr.String(), "\t", 5)
			return items[4]
		}
	}

	return ""
}

func SetMinimumTTL(msg *dns.Msg, minimumTTL uint32) {
	if minimumTTL == 0 {
		return
	}
	// Raise TTLs across the whole message: the cache lifetime is derived
	// from the shortest record TTL over Answer/Ns/Extra, so a low-TTL SOA in
	// the authority section would otherwise bypass the configured minimum.
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			if rr.Header().Ttl < minimumTTL {
				rr.Header().Ttl = minimumTTL
			}
		}
	}
}

func SetTTLByMap(msg *dns.Msg, domainTTLMap map[string]uint32) {
	if len(domainTTLMap) == 0 {
		return
	}
	// Apply the override across Answer/Ns/Extra (skipping OPT, whose TTL field
	// carries extension flags), so records such as an authority SOA observe the
	// same override as answers, matching SetMinimumTTL's section coverage.
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			if rr.Header().Rrtype == dns.TypeOPT {
				continue
			}
			name := strings.TrimSuffix(rr.Header().Name, ".")
			for k, v := range domainTTLMap {
				if IsDomainMatchRule(k, name) {
					rr.Header().Ttl = v
				}
			}
		}
	}
}
