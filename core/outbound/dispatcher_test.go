package outbound

import (
	"sync"
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
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
