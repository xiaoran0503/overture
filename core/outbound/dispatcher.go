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
	var ActiveClientBundle *clients.RemoteClientBundle

	if d.OnlyPrimaryDNS || d.isSelectDomain(PrimaryClientBundle, d.DomainPrimaryList) {
		ActiveClientBundle = PrimaryClientBundle
		return ActiveClientBundle.Exchange(true, true)
	}

	if ok := d.isExchangeForIPv6(query) || d.isSelectDomain(AlternativeClientBundle, d.DomainAlternativeList); ok {
		ActiveClientBundle = AlternativeClientBundle
		return ActiveClientBundle.Exchange(true, true)
	}

	ActiveClientBundle = d.selectByIPNetwork(PrimaryClientBundle, AlternativeClientBundle)

	// Only try to Cache result before return
	ActiveClientBundle.CacheResultIfNeeded()
	return ActiveClientBundle.GetResponseMessage()
}

func (d *Dispatcher) isExchangeForIPv6(query *dns.Msg) bool {
	if query.Question[0].Qtype == dns.TypeAAAA && d.RedirectIPv6Record {
		log.Debug("Finally use alternative DNS")
		return true
	}

	return false
}

func (d *Dispatcher) isSelectDomain(rcb *clients.RemoteClientBundle, dt matcher.Matcher) bool {
	if dt != nil {
		qn := rcb.GetFirstQuestionDomain()

		if dt.Has(qn) {
			log.WithFields(log.Fields{
				"DNS":      rcb.Name,
				"question": qn,
				"domain":   qn,
			}).Debug("Matched")
			log.Debugf("Finally use %s DNS", rcb.Name)
			return true
		}

		log.Debugf("Domain %s match fail", rcb.Name)
	} else {
		log.Debug("Domain matcher is nil, not checking")
	}

	return false
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
			} else {
				log.Debug("primaryDNS response has no answer section but exist, finally use alternativeDNS")
				waitAlternateResp()
				return AlternativeClientBundle
			}
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
