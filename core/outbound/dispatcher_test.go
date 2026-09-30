package outbound

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
	matcherfull "github.com/shawn1m/overture/core/matcher/full"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
	"github.com/shawn1m/overture/core/outbound/routecache"
	"golang.org/x/sync/singleflight"
)

// countingResolver answers and counts upstream exchanges.
type countingResolver struct {
	resp *dns.Msg
	mu   sync.Mutex
	n    int
}

func (s *countingResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
	r := s.resp.Copy()
	r.SetReply(q)
	return r, nil
}
func (s *countingResolver) Init() error  { return nil }
func (s *countingResolver) Close() error { return nil }

func countingA(msg *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.Answer = []dns.RR{mustDispRR("example.com. 60 IN A 192.0.2.1")}
	return m
}

func mustDispRR(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		panic(err)
	}
	return rr
}

func TestExchangeSingleFlightMergesConcurrentMisses(t *testing.T) {
	c := cache.New(64, "", 0)
	r := &countingResolver{resp: countingA(nil)}
	d := &Dispatcher{
		PrimaryDNS:        []*common.DNSUpstream{{Name: "t", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:    []*common.DNSUpstream{},
		OnlyPrimaryDNS:    true,
		MinimumTTL:        0,
		Cache:             c,
		primaryResolvers:  []resolver.Resolver{r},
		cacheSingleFlight: new(singleflight.Group),
	}

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)

	const callers = 16
	var wg sync.WaitGroup
	results := make([]*dns.Msg, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = d.Exchange(q, "127.0.0.1")
		}(i)
	}
	wg.Wait()

	for i, m := range results {
		if m == nil || len(m.Answer) != 1 {
			t.Fatalf("caller %d got no answer: %v", i, m)
		}
	}
	r.mu.Lock()
	exchanges := r.n
	r.mu.Unlock()
	if exchanges != 1 {
		t.Fatalf("upstream exchanges = %d, want 1 (singleflight merge)", exchanges)
	}

	// A follow-up query is served from the cache, not the upstream.
	q2 := new(dns.Msg)
	q2.SetQuestion("example.com.", dns.TypeA)
	if m := d.Exchange(q2, "127.0.0.1"); m == nil || len(m.Answer) != 1 {
		t.Fatalf("cached follow-up query failed: %v", m)
	}
	r.mu.Lock()
	after := r.n
	r.mu.Unlock()
	if after != 1 {
		t.Fatalf("upstream exchanges after cache fill = %d, want 1", after)
	}
}

func TestExchangeNilResponseNoPanic(t *testing.T) {
	d := &Dispatcher{
		PrimaryDNS:        []*common.DNSUpstream{{Name: "t", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:    []*common.DNSUpstream{},
		OnlyPrimaryDNS:    true,
		Cache:             cache.New(64, "", 0),
		primaryResolvers:  []resolver.Resolver{&nilResolver{}},
		cacheSingleFlight: new(singleflight.Group),
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if m := d.Exchange(q, "127.0.0.1"); m != nil {
		t.Fatalf("nil upstream response should yield nil, got %v", m)
	}
}

// nilResolver always returns no response.
type nilResolver struct{}

func (s *nilResolver) Exchange(q *dns.Msg) (*dns.Msg, error) { return nil, nil }
func (s *nilResolver) Init() error                           { return nil }
func (s *nilResolver) Close() error                          { return nil }

type countingMatcher struct {
	mu    sync.Mutex
	hits  int
	names map[string]struct{}
}

func newCountingMatcher(names ...string) *countingMatcher {
	m := &countingMatcher{names: make(map[string]struct{})}
	for _, n := range names {
		m.names[strings.ToLower(n)] = struct{}{}
	}
	return m
}

func (m *countingMatcher) Insert(s string) error {
	m.mu.Lock()
	m.names[strings.ToLower(s)] = struct{}{}
	m.mu.Unlock()
	return nil
}

func (m *countingMatcher) Has(s string) bool {
	m.mu.Lock()
	m.hits++
	_, ok := m.names[strings.ToLower(s)]
	m.mu.Unlock()
	return ok
}

func (m *countingMatcher) Name() string { return "counting" }

func (m *countingMatcher) hitCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hits
}

func questionA(name string) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	return q
}

func testDispatcher(primary resolver.Resolver, matcherNames ...string) (*Dispatcher, *countingMatcher) {
	m := newCountingMatcher(matcherNames...)
	d := &Dispatcher{
		PrimaryDNS:           []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:       []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
		DomainPrimaryList:    m,
		primaryResolvers:     []resolver.Resolver{primary},
		alternativeResolvers: []resolver.Resolver{&nilResolver{}},
		routeCache:           routecache.New(64, 600),
	}
	return d, m
}

func TestRouteCacheSkipsDomainListOnSecondQuery(t *testing.T) {
	r := &countingResolver{resp: countingA(nil)}
	d, m := testDispatcher(r, "example.com")
	q := questionA("example.com.")
	if got := d.Exchange(q, "127.0.0.1"); got == nil || len(got.Answer) != 1 {
		t.Fatalf("first: %v", got)
	}
	firstHits := m.hitCount()
	if firstHits == 0 {
		t.Fatal("matcher was not consulted on the first query")
	}
	if got := d.Exchange(questionA("example.com."), "127.0.0.1"); got == nil || len(got.Answer) != 1 {
		t.Fatalf("second: %v", got)
	}
	if m.hitCount() != firstHits {
		t.Fatalf("matcher hits %d after second query, want %d (route cache should skip the list)", m.hitCount(), firstHits)
	}
}

func TestRouteCacheDisabledStillScans(t *testing.T) {
	r := &countingResolver{resp: countingA(nil)}
	m := newCountingMatcher("example.com")
	d := &Dispatcher{
		PrimaryDNS:           []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:       []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
		DomainPrimaryList:    m,
		primaryResolvers:     []resolver.Resolver{r},
		alternativeResolvers: []resolver.Resolver{&nilResolver{}},
	}
	d.Exchange(questionA("example.com."), "127.0.0.1")
	d.Exchange(questionA("example.com."), "127.0.0.1")
	if m.hitCount() < 2 {
		t.Fatalf("disabled route cache still must walk the list, hits=%d", m.hitCount())
	}
}

func TestRouteCacheUndecidedSkipsListsButStillClassifies(t *testing.T) {
	primary := &countingResolver{resp: countingA(nil)}
	pm := newCountingMatcher()
	am := newCountingMatcher()
	_, cidr, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		PrimaryDNS:            []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:        []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
		DomainPrimaryList:     pm,
		DomainAlternativeList: am,
		IPNetworkPrimarySet:   common.NewIPSet([]*net.IPNet{cidr}),
		primaryResolvers:      []resolver.Resolver{primary},
		alternativeResolvers:  []resolver.Resolver{&nilResolver{}},
		routeCache:            routecache.New(64, 600),
	}
	if got := d.Exchange(questionA("example.com."), "127.0.0.1"); got == nil {
		t.Fatal("first query nil")
	}
	if pm.hitCount() == 0 || am.hitCount() == 0 {
		t.Fatal("IP path must walk both domain lists on the first query")
	}
	pHits, aHits := pm.hitCount(), am.hitCount()
	primary.mu.Lock()
	n1 := primary.n
	primary.mu.Unlock()
	if n1 == 0 {
		t.Fatal("first query must classify via primary")
	}
	if got := d.Exchange(questionA("example.com."), "127.0.0.1"); got == nil {
		t.Fatal("second query nil")
	}
	if pm.hitCount() != pHits || am.hitCount() != aHits {
		t.Fatalf("second query walked lists again (primary %d->%d alternative %d->%d)", pHits, pm.hitCount(), aHits, am.hitCount())
	}
	primary.mu.Lock()
	n2 := primary.n
	primary.mu.Unlock()
	if n2 != n1+1 {
		t.Fatalf("undecided cache must still IP-classify, primary exchanges %d -> %d", n1, n2)
	}
}

func TestRouteCacheDoesNotCacheIPDecision(t *testing.T) {
	primary := &countingResolver{resp: countingA(nil)}
	_, cidr, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	d := &Dispatcher{
		PrimaryDNS:            []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:        []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
		DomainPrimaryList:     &matcherfull.Map{DataMap: map[string]struct{}{}},
		DomainAlternativeList: &matcherfull.Map{DataMap: map[string]struct{}{}},
		IPNetworkPrimarySet:   common.NewIPSet([]*net.IPNet{cidr}),
		primaryResolvers:      []resolver.Resolver{primary},
		alternativeResolvers:  []resolver.Resolver{&nilResolver{}},
		routeCache:            routecache.New(64, 600),
	}
	d.Exchange(questionA("example.com."), "127.0.0.1")
	qtxt := new(dns.Msg)
	qtxt.SetQuestion("example.com.", dns.TypeTXT)
	d.Exchange(qtxt, "127.0.0.1")
	primary.mu.Lock()
	n := primary.n
	primary.mu.Unlock()
	if n < 2 {
		t.Fatalf("IP classify must run per query, primary exchanges=%d", n)
	}
}

func TestRouteCacheOnOffSameGroup(t *testing.T) {
	_, cidr, err := net.ParseCIDR("192.0.2.0/24")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		primary   []string
		alt       []string
		qtype     uint16
		ipv6      bool
		wantName  string
		ipPrimary bool
	}{
		{"domain primary", []string{"example.com"}, nil, dns.TypeA, false, "p", false},
		{"domain alternative", nil, []string{"example.com"}, dns.TypeA, false, "a", false},
		{"primary wins both lists", []string{"example.com"}, []string{"example.com"}, dns.TypeA, false, "p", false},
		{"ipv6 redirect", nil, nil, dns.TypeAAAA, true, "a", false},
		{"ipnet china", nil, nil, dns.TypeA, false, "p", true},
	}
	for _, tc := range cases {
		for _, cacheOn := range []bool{false, true} {
			name := tc.name + "/cacheOff"
			if cacheOn {
				name = tc.name + "/cacheOn"
			}
			t.Run(name, func(t *testing.T) {
				pr := &countingResolver{resp: countingA(nil)}
				ar := &countingResolver{resp: countingA(nil)}
				d := &Dispatcher{
					PrimaryDNS:            []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
					AlternativeDNS:        []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
					DomainPrimaryList:     newCountingMatcher(tc.primary...),
					DomainAlternativeList: newCountingMatcher(tc.alt...),
					RedirectIPv6Record:    tc.ipv6,
					primaryResolvers:      []resolver.Resolver{pr},
					alternativeResolvers:  []resolver.Resolver{ar},
				}
				if tc.ipPrimary {
					d.IPNetworkPrimarySet = common.NewIPSet([]*net.IPNet{cidr})
				}
				if cacheOn {
					d.routeCache = routecache.New(64, 600)
				}
				q := new(dns.Msg)
				q.SetQuestion("example.com.", tc.qtype)
				if got := d.Exchange(q, "127.0.0.1"); got == nil {
					t.Fatal("nil response")
				}
				if cacheOn {
					q2 := new(dns.Msg)
					q2.SetQuestion("example.com.", tc.qtype)
					if got := d.Exchange(q2, "127.0.0.1"); got == nil {
						t.Fatal("nil second response")
					}
				}
				pr.mu.Lock()
				pn := pr.n
				pr.mu.Unlock()
				ar.mu.Lock()
				an := ar.n
				ar.mu.Unlock()
				switch tc.wantName {
				case "p":
					if pn == 0 {
						t.Fatalf("want primary, primary=%d alternative=%d", pn, an)
					}
				case "a":
					if an == 0 {
						t.Fatalf("want alternative, primary=%d alternative=%d", pn, an)
					}
				}
			})
		}
	}
}

func TestRouteCacheAAAANotHijackedByUndecidedA(t *testing.T) {
	pr := &countingResolver{resp: countingA(nil)}
	ar := &countingResolver{resp: countingA(nil)}
	d := &Dispatcher{
		PrimaryDNS:            []*common.DNSUpstream{{Name: "p", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}},
		AlternativeDNS:        []*common.DNSUpstream{{Name: "a", Address: "127.0.0.1:54", Protocol: "udp", Timeout: 3}},
		DomainPrimaryList:     newCountingMatcher(),
		DomainAlternativeList: newCountingMatcher(),
		RedirectIPv6Record:    true,
		primaryResolvers:      []resolver.Resolver{pr},
		alternativeResolvers:  []resolver.Resolver{ar},
		routeCache:            routecache.New(64, 600),
	}
	if d.Exchange(questionA("example.com."), "127.0.0.1") == nil {
		t.Fatal("A nil")
	}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeAAAA)
	if d.Exchange(q, "127.0.0.1") == nil {
		t.Fatal("AAAA nil")
	}
	ar.mu.Lock()
	an := ar.n
	ar.mu.Unlock()
	if an == 0 {
		t.Fatal("AAAA + ipv6 redirect must use alternative even after A was undecided")
	}
}
