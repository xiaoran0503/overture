// Copyright (c) 2016 shawn1m. All rights reserved.
// Use of this source code is governed by The MIT License (MIT) that can be
// found in the LICENSE file.

// Package common provides common functions.
package common

import (
	"net"
	"regexp"
	"strings"
	"sync"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

var ReservedIPNetworkList = getReservedIPNetworkList()

// compiledRegexCache memoizes compiled patterns so per-query TTL and hosts
// lookups do not recompile the same regular expression on every record.
// Failed compilations are cached as nil so a broken pattern is compiled at
// most once instead of on every query.
var compiledRegexCache sync.Map // pattern string -> *regexp.Regexp (nil on compile error)

func IsDomainMatchRule(pattern string, domain string) bool {
	if cached, ok := compiledRegexCache.Load(pattern); ok {
		re, _ := cached.(*regexp.Regexp)
		if re == nil {
			return false
		}
		return re.MatchString(domain)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Warnf("Error matching domain %s with pattern %s: %s", domain, pattern, err)
		compiledRegexCache.Store(pattern, nil)
		return false
	}
	compiledRegexCache.Store(pattern, re)
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
	for _, a := range msg.Answer {
		name := a.Header().Name[:len(a.Header().Name)-1]
		for k, v := range domainTTLMap {
			if IsDomainMatchRule(k, name) {
				a.Header().Ttl = v
			}
		}
	}
}
