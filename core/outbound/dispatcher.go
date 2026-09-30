package outbound

import (
	"errors"
	"net"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"

	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/hosts"
	"github.com/shawn1m/overture/core/matcher"
	"github.com/shawn1m/overture/core/outbound/clients"
	"github.com/shawn1m/overture/core/outbound/health"
	"github.com/shawn1m/overture/core/outbound/routecache"
)

type Dispatcher struct {
	PrimaryDNS     []*common.DNSUpstream
	AlternativeDNS []*common.DNSUpstream
	OnlyPrimaryDNS bool

	WhenPrimaryDNSAnswerNoneUse string
	IPNetworkPrimarySet         *common.IPSet
	IPNetworkAlternativeSet     *common.IPSet
	DomainPrimaryList           matcher.Matcher
	DomainAlternativeList       matcher.Matcher
	RedirectIPv6Record          bool
	AlternativeDNSConcurrent    bool

	MinimumTTL       int
	DomainTTLMap     map[string]uint32
	DomainECSMap     common.DomainECSMap
	UpstreamFailover string

	Hosts *hosts.Hosts
	Cache *cache.Cache

	primaryResolvers     []resolver.Resolver
	alternativeResolvers []resolver.Resolver

	// cacheSingleFlight merges concurrent identical cache misses into one
	// upstream exchange. Pointer so value copies of Dispatcher never share a
	// sync.Mutex. Initialized by Init.
	cacheSingleFlight *singleflight.Group

	HealthCheck health.Options
	health      *health.Checker

	RouteCacheSize int
	RouteCacheTTL  int
	routeCache     *routecache.Cache
}

// errNoUpstreamResponse marks a merged upstream exchange that produced no
// answer; singleflight remembers it for the in-flight batch only.
var errNoUpstreamResponse = errors.New("no response from upstream")

func createResolver(ul []*common.DNSUpstream) (resolvers []resolver.Resolver) {
	resolvers = make([]resolver.Resolver, len(ul))
	for i, u := range ul {
		resolvers[i] = resolver.NewResolver(u)
	}
	return resolvers
}

func (d *Dispatcher) Init() {
	d.primaryResolvers = createResolver(d.PrimaryDNS)
	d.alternativeResolvers = createResolver(d.AlternativeDNS)
	d.cacheSingleFlight = new(singleflight.Group)

	var targets []health.Target
	for i, u := range d.PrimaryDNS {
		targets = append(targets, health.Target{Group: "Primary", Upstream: u, Resolver: d.primaryResolvers[i]})
	}
	for i, u := range d.AlternativeDNS {
		targets = append(targets, health.Target{Group: "Alternative", Upstream: u, Resolver: d.alternativeResolvers[i]})
	}
	d.health = health.New(d.HealthCheck, targets)
	d.health.Start()
	d.routeCache = routecache.New(d.RouteCacheSize, d.RouteCacheTTL)
}

func (d *Dispatcher) Exchange(query *dns.Msg, inboundIP string) *dns.Msg {
	PrimaryClientBundle := clients.NewClientBundleWithHealth(query, d.PrimaryDNS, d.primaryResolvers, inboundIP, d.MinimumTTL, d.Cache, "Primary", d.DomainTTLMap, d.health)
	PrimaryClientBundle.Configure(d.UpstreamFailover, d.DomainECSMap)
	AlternativeClientBundle := clients.NewClientBundleWithHealth(query, d.AlternativeDNS, d.alternativeResolvers, inboundIP, d.MinimumTTL, d.Cache, "Alternative", d.DomainTTLMap, d.health)
	AlternativeClientBundle.Configure(d.UpstreamFailover, d.DomainECSMap)

	localClient := clients.NewLocalClient(query, d.Hosts, d.MinimumTTL, d.DomainTTLMap)
	resp := localClient.Exchange()
	if resp != nil {
		return resp
	}

	for _, cb := range []*clients.RemoteClientBundle{PrimaryClientBundle, AlternativeClientBundle} {
		resp := cb.ExchangeFromCache()
		if resp != nil {
			return resp
		}
	}

	// Concurrent identical queries that miss the cache would each exchange
	// with the upstream. Merge them by cache key (roadmap phase 2: dispatcher
	// key dedup) so N simultaneous misses produce one upstream exchange, then
	// hand each caller its own copy: callers mutate Compress/Truncated on the
	// returned message and must never share it.
	if d.Cache != nil && d.cacheSingleFlight != nil {
		key := cache.Key(query.Question[0], common.GetEDNSClientSubnetIP(query))
		v, err, _ := d.cacheSingleFlight.Do(key, func() (interface{}, error) {
			// Re-check the cache inside the merged call: the first caller may
			// find an entry another batch filled while we were waiting.
			for _, cb := range []*clients.RemoteClientBundle{PrimaryClientBundle, AlternativeClientBundle} {
				if resp := cb.ExchangeFromCache(); resp != nil {
					return resp, nil
				}
			}
			m := d.routeAndExchange(query, inboundIP, PrimaryClientBundle, AlternativeClientBundle)
			if m == nil {
				return nil, errNoUpstreamResponse
			}
			return m, nil
		})
		if err != nil {
			return nil
		}
		return v.(*dns.Msg).Copy()
	}

	return d.routeAndExchange(query, inboundIP, PrimaryClientBundle, AlternativeClientBundle)
}

// routeAndExchange performs the routing decision and upstream exchange that
// follows a cache miss. Shared by the direct (cache disabled) and the merged
// (singleflight) paths.
func (d *Dispatcher) routeAndExchange(query *dns.Msg, inboundIP string, PrimaryClientBundle, AlternativeClientBundle *clients.RemoteClientBundle) *dns.Msg {
	if d.OnlyPrimaryDNS {
		return PrimaryClientBundle.Exchange(true, true)
	}

	qname := questionDomain(query)
	qtype := uint16(0)
	if len(query.Question) > 0 {
		qtype = query.Question[0].Qtype
	}
	cached, hit := d.routeCache.Lookup(qname)

	// Lookup order: Primary cache -> IPv6 -> Alternative cache -> undecided
	// cache, so an A-query's undecided entry cannot skip AAAA redirect.
	if hit && cached == routeCachePrimary {
		log.Debugf("Route cache hit %s -> Primary", qname)
		recordRoute(routeReasonDomainPrimary)
		return PrimaryClientBundle.Exchange(true, true)
	}
	if qtype == dns.TypeAAAA && d.RedirectIPv6Record {
		recordRoute(routeReasonIPv6)
		return AlternativeClientBundle.Exchange(true, true)
	}
	if hit && cached == routeCacheAlternative {
		log.Debugf("Route cache hit %s -> Alternative", qname)
		recordRoute(routeReasonDomainAlternative)
		return AlternativeClientBundle.Exchange(true, true)
	}
	if hit && cached == routeCacheUndecided {
		log.Debugf("Route cache hit %s -> undecided", qname)
		recordRoute(routeReasonIPNet)
		return d.exchangeIPNetwork(PrimaryClientBundle, AlternativeClientBundle)
	}

	dec := decideDomainRoute(qname, qtype, d.RedirectIPv6Record, d.DomainPrimaryList, d.DomainAlternativeList)
	switch dec {
	case decisionPrimaryDomain:
		d.routeCache.Put(qname, routeCachePrimary)
		recordRoute(routeReasonDomainPrimary)
		return PrimaryClientBundle.Exchange(true, true)
	case decisionIPv6:
		recordRoute(routeReasonIPv6)
		return AlternativeClientBundle.Exchange(true, true)
	case decisionAlternativeDomain:
		d.routeCache.Put(qname, routeCacheAlternative)
		recordRoute(routeReasonDomainAlternative)
		return AlternativeClientBundle.Exchange(true, true)
	default:
		d.routeCache.Put(qname, routeCacheUndecided)
		recordRoute(routeReasonIPNet)
		return d.exchangeIPNetwork(PrimaryClientBundle, AlternativeClientBundle)
	}
}

func (d *Dispatcher) exchangeIPNetwork(PrimaryClientBundle, AlternativeClientBundle *clients.RemoteClientBundle) *dns.Msg {
	active := d.selectByIPNetwork(PrimaryClientBundle, AlternativeClientBundle)
	active.CacheResultIfNeeded()
	return active.GetResponseMessage()
}

func (d *Dispatcher) selectByIPNetwork(PrimaryClientBundle, AlternativeClientBundle *clients.RemoteClientBundle) *clients.RemoteClientBundle {
	// Both senders must finish even when the other result is selected.
	primaryOut := make(chan *dns.Msg, 1)
	alternateOut := make(chan *dns.Msg, 1)
	go func() {
		primaryOut <- PrimaryClientBundle.Exchange(false, true)
	}()
	alternateFunc := func() {
		alternateOut <- AlternativeClientBundle.Exchange(false, true)
	}
	waitAlternateResp := func() {
		if !d.AlternativeDNSConcurrent {
			go alternateFunc()
		}
		<-alternateOut
	}
	if d.AlternativeDNSConcurrent {
		go alternateFunc()
	}
	primaryResponse := <-primaryOut

	if primaryResponse != nil {
		if len(primaryResponse.Answer) == 0 {
			if d.WhenPrimaryDNSAnswerNoneUse != "alternativeDNS" && d.WhenPrimaryDNSAnswerNoneUse != "AlternativeDNS" {
				log.Debug("primaryDNS response has no answer section but exist, finally use primaryDNS")
				return PrimaryClientBundle
			}
			log.Debug("primaryDNS response has no answer section but exist, finally use alternativeDNS")
			waitAlternateResp()
			return AlternativeClientBundle
		}
	} else {
		log.Debug("Primary DNS return nil, finally use alternative DNS")
		waitAlternateResp()
		return AlternativeClientBundle
	}

	for _, a := range PrimaryClientBundle.GetResponseMessage().Answer {
		log.Debug("Try to match response ip address with IP network")
		var ip net.IP
		if a.Header().Rrtype == dns.TypeA {
			ip = a.(*dns.A).A
		} else if a.Header().Rrtype == dns.TypeAAAA {
			ip = a.(*dns.AAAA).AAAA
		} else {
			continue
		}
		if d.IPNetworkPrimarySet.Contains(ip, true, "primary") {
			log.Debug("Finally use primary DNS")
			return PrimaryClientBundle
		}
		if d.IPNetworkAlternativeSet.Contains(ip, true, "alternative") {
			log.Debug("Finally use alternative DNS")
			waitAlternateResp()
			return AlternativeClientBundle
		}
	}
	log.Debug("IP network match failed, finally use alternative DNS")
	waitAlternateResp()
	return AlternativeClientBundle
}

// Close releases resolver and cache resources after an inbound server stops.
func (d *Dispatcher) Close() {
	d.health.Stop()
	for _, group := range [][]resolver.Resolver{d.primaryResolvers, d.alternativeResolvers} {
		for _, item := range group {
			if item != nil {
				_ = item.Close()
			}
		}
	}
	if d.Cache != nil {
		_ = d.Cache.Close()
	}
}
