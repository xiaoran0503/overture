/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */

package clients

import (
	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
	log "github.com/sirupsen/logrus"

	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
)

type RemoteClientBundle struct {
	responseMessage *dns.Msg
	questionMessage *dns.Msg

	clients []*RemoteClient

	dnsUpstreams []*common.DNSUpstream
	inboundIP    string
	minimumTTL   int
	domainTTLMap map[string]uint32

	cache *cache.Cache
	Name  string

	dnsResolvers []resolver.Resolver
	health       UpstreamHealth
}

func NewClientBundle(q *dns.Msg, ul []*common.DNSUpstream, resolvers []resolver.Resolver, ip string, minimumTTL int, cache *cache.Cache, name string, domainTTLMap map[string]uint32) *RemoteClientBundle {
	return NewClientBundleWithHealth(q, ul, resolvers, ip, minimumTTL, cache, name, domainTTLMap, nil)
}

func NewClientBundleWithHealth(q *dns.Msg, ul []*common.DNSUpstream, resolvers []resolver.Resolver, ip string, minimumTTL int, cache *cache.Cache, name string, domainTTLMap map[string]uint32, h UpstreamHealth) *RemoteClientBundle {
	cb := &RemoteClientBundle{questionMessage: q.Copy(), dnsUpstreams: ul, dnsResolvers: resolvers, inboundIP: ip, minimumTTL: minimumTTL, cache: cache, Name: name, domainTTLMap: domainTTLMap, health: h}

	for i, u := range ul {
		c := NewClient(cb.questionMessage, u, cb.dnsResolvers[i], cb.inboundIP, cb.cache)
		c.health = h
		cb.clients = append(cb.clients, c)
	}

	return cb
}

func (cb *RemoteClientBundle) activeClients() []*RemoteClient {
	if cb.health == nil {
		return cb.clients
	}
	var up []*RemoteClient
	for _, c := range cb.clients {
		if cb.health.Healthy(c.dnsUpstream) {
			up = append(up, c)
		} else {
			log.Debugf("Skipping unhealthy upstream %s (%s)", c.dnsUpstream.Name, c.dnsUpstream.Address)
		}
	}
	if len(up) == 0 {
		log.Warnf("%s: all upstreams unhealthy, querying all (fail-open)", cb.Name)
		return cb.clients
	}
	return up
}

func (cb *RemoteClientBundle) Exchange(isCache bool, isLog bool) *dns.Msg {
	clients := cb.activeClients()
	ch := make(chan *RemoteClient, len(clients))

	for _, o := range clients {
		go func(c *RemoteClient, ch chan *RemoteClient) {
			c.Exchange(isLog)
			ch <- c
		}(o, ch)
	}

	var ec *RemoteClient

	for i := 0; i < len(clients); i++ {
		c := <-ch
		if c != nil {
			ec = c
			if ec.responseMessage != nil && len(ec.responseMessage.Answer) > 0 {
				break
			}
			log.Debugf("DNSUpstream %s returned a response without an answer section; waiting for the next upstream", ec.dnsUpstream.Address)
		}
	}

	if ec != nil && ec.responseMessage != nil {
		cb.responseMessage = ec.responseMessage
		cb.questionMessage = ec.questionMessage

		common.SetMinimumTTL(cb.responseMessage, uint32(cb.minimumTTL))
		common.SetTTLByMap(cb.responseMessage, cb.domainTTLMap)

		if isCache {
			cb.CacheResultIfNeeded()
		}
	}

	return cb.responseMessage
}

func (cb *RemoteClientBundle) ExchangeFromCache() *dns.Msg {
	for _, o := range cb.clients {
		cb.responseMessage = o.ExchangeFromCache()
		if cb.responseMessage != nil {
			return cb.responseMessage
		}
	}
	return cb.responseMessage
}

func (cb *RemoteClientBundle) CacheResultIfNeeded() {
	if cb.cache != nil {
		cb.cache.InsertMessage(cache.Key(cb.questionMessage.Question[0], common.GetEDNSClientSubnetIP(cb.questionMessage)), cb.responseMessage, uint32(cb.minimumTTL))
	}
}

func (cb *RemoteClientBundle) IsType(t uint16) bool {
	return t == cb.questionMessage.Question[0].Qtype
}

func (cb *RemoteClientBundle) GetFirstQuestionDomain() string {
	return cb.questionMessage.Question[0].Name[:len(cb.questionMessage.Question[0].Name)-1]
}

func (cb *RemoteClientBundle) GetResponseMessage() *dns.Msg {
	return cb.responseMessage
}
