package clients

import (
	"errors"
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
)

// stubResolver returns a programmable response or error.
type stubResolver struct {
	resp *dns.Msg
	err  error
}

func (s *stubResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.resp == nil {
		return nil, nil
	}
	r := s.resp.Copy()
	r.SetReply(q)
	return r, nil
}
func (s *stubResolver) Init() error  { return nil }
func (s *stubResolver) Close() error { return nil }

func testUpstream(policy string) *common.DNSUpstream {
	return &common.DNSUpstream{
		Name:     "t",
		Address:  "127.0.0.1:53",
		Protocol: "udp",
		Timeout:  3,
		EDNSClientSubnet: &common.EDNSClientSubnetType{
			Policy:     policy,
			ExternalIP: "203.0.113.9",
			NoCookie:   false,
		},
	}
}

func aReply() *dns.Msg {
	m := new(dns.Msg)
	m.Answer = []dns.RR{mustRR("example.com. 60 IN A 192.0.2.1")}
	return m
}

func mustRR(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		panic(err)
	}
	return rr
}

func TestGetEDNSClientSubnetIPAutoExternal(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("auto"), &stubResolver{}, "203.0.113.5", nil)
	if c.ednsClientSubnetIP != "203.0.113.5" {
		t.Fatalf("auto with external inbound IP: got %q, want inbound IP", c.ednsClientSubnetIP)
	}
}

func TestGetEDNSClientSubnetIPAutoReservedFallsBack(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("auto"), &stubResolver{}, "192.168.1.1", nil)
	if c.ednsClientSubnetIP != "203.0.113.9" {
		t.Fatalf("auto with reserved inbound IP: got %q, want externalIP", c.ednsClientSubnetIP)
	}
}

func TestGetEDNSClientSubnetIPManual(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("manual"), &stubResolver{}, "192.168.1.1", nil)
	if c.ednsClientSubnetIP != "203.0.113.9" {
		t.Fatalf("manual: got %q, want externalIP", c.ednsClientSubnetIP)
	}
}

func TestGetEDNSClientSubnetIPDisable(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("disable"), &stubResolver{}, "192.168.1.1", nil)
	if c.ednsClientSubnetIP != "" {
		t.Fatalf("disable: got %q, want empty", c.ednsClientSubnetIP)
	}
}

func TestExchangeReturnsNilOnResolverError(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("disable"), &stubResolver{err: errors.New("boom")}, "127.0.0.1", nil)
	if got := c.Exchange(false); got != nil {
		t.Fatal("Exchange should return nil when the resolver errors")
	}
}

func TestExchangeReturnsNilOnNilResponse(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	c := NewClient(q, testUpstream("disable"), &stubResolver{}, "127.0.0.1", nil)
	if got := c.Exchange(false); got != nil {
		t.Fatal("Exchange should return nil when the resolver returns no message")
	}
}

func TestBundleExchangePicksFirstAnswer(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	b := NewClientBundle(q,
		[]*common.DNSUpstream{testUpstream("disable"), testUpstream("disable")},
		[]resolver.Resolver{&stubResolver{resp: aReply()}, &stubResolver{resp: aReply()}},
		"127.0.0.1", 0, nil, "Test", nil)
	got := b.Exchange(false, false)
	if got == nil || len(got.Answer) != 1 {
		t.Fatalf("bundle exchange: got %v, want 1 answer", got)
	}
}

func TestBundleExchangeEmptyAnswerFallsThrough(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	empty := new(dns.Msg)
	b := NewClientBundle(q,
		[]*common.DNSUpstream{testUpstream("disable"), testUpstream("disable")},
		[]resolver.Resolver{&stubResolver{resp: empty}, &stubResolver{resp: aReply()}},
		"127.0.0.1", 0, nil, "Test", nil)
	got := b.Exchange(false, false)
	if got == nil || len(got.Answer) != 1 {
		t.Fatalf("bundle should fall through an answer-less upstream: got %v, want 1 answer", got)
	}
}

func TestBundleExchangeAllEmptyReturnsMessage(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	empty := new(dns.Msg)
	b := NewClientBundle(q,
		[]*common.DNSUpstream{testUpstream("disable"), testUpstream("disable")},
		[]resolver.Resolver{&stubResolver{resp: empty}, &stubResolver{resp: empty}},
		"127.0.0.1", 0, nil, "Test", nil)
	if got := b.Exchange(false, false); got == nil {
		t.Fatal("bundle should still return the last upstream message when all are answer-less")
	}
}

func TestBundleExchangeFromCacheHit(t *testing.T) {
	c := cache.New(100, "", 0)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	key := cache.Key(q.Question[0], "")
	c.InsertMessage(key, aReply(), 60)

	b := NewClientBundle(q,
		[]*common.DNSUpstream{testUpstream("disable")},
		[]resolver.Resolver{&stubResolver{resp: aReply()}},
		"127.0.0.1", 0, c, "Test", nil)
	if got := b.ExchangeFromCache(); got == nil || len(got.Answer) != 1 {
		t.Fatalf("cache hit should return the cached message, got %v", got)
	}
}

func TestBundleCacheResultIfNeededStoresForNextQuery(t *testing.T) {
	c := cache.New(100, "", 0)
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	b := NewClientBundle(q,
		[]*common.DNSUpstream{testUpstream("disable")},
		[]resolver.Resolver{&stubResolver{resp: aReply()}},
		"127.0.0.1", 0, c, "Test", nil)
	if got := b.Exchange(false, false); got == nil {
		t.Fatal("exchange failed")
	}
	b.CacheResultIfNeeded()
	if got := b.ExchangeFromCache(); got == nil {
		t.Fatal("cached result should be retrievable on the next query")
	}
}

type countingStub struct {
	stubResolver
	n int
}

func (s *countingStub) Exchange(q *dns.Msg) (*dns.Msg, error) {
	s.n++
	return s.stubResolver.Exchange(q)
}

type mapHealth struct {
	down map[string]bool
}

func (m *mapHealth) Healthy(u *common.DNSUpstream) bool { return u == nil || !m.down[u.Address] }
func (m *mapHealth) Record(*common.DNSUpstream, bool)   {}

func TestBundleSkipsUnhealthyUpstream(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	dead := testUpstream("disable")
	dead.Address = "127.0.0.1:1"
	live := testUpstream("disable")
	live.Address = "127.0.0.1:2"
	deadStub := &countingStub{stubResolver: stubResolver{err: errors.New("dead")}}
	liveStub := &countingStub{stubResolver: stubResolver{resp: aReply()}}
	h := &mapHealth{down: map[string]bool{dead.Address: true}}
	b := NewClientBundleWithHealth(q,
		[]*common.DNSUpstream{dead, live},
		[]resolver.Resolver{deadStub, liveStub},
		"127.0.0.1", 0, nil, "Test", nil, h)
	got := b.Exchange(false, false)
	if got == nil || len(got.Answer) != 1 {
		t.Fatalf("bundle should use the healthy upstream, got %v", got)
	}
	if deadStub.n != 0 {
		t.Fatalf("unhealthy upstream was queried %d times", deadStub.n)
	}
	if liveStub.n != 1 {
		t.Fatalf("healthy upstream queries = %d, want 1", liveStub.n)
	}
}

func TestBundleFailOpenWhenAllUnhealthy(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	a := testUpstream("disable")
	a.Address = "127.0.0.1:1"
	bUp := testUpstream("disable")
	bUp.Address = "127.0.0.1:2"
	h := &mapHealth{down: map[string]bool{a.Address: true, bUp.Address: true}}
	bundle := NewClientBundleWithHealth(q,
		[]*common.DNSUpstream{a, bUp},
		[]resolver.Resolver{&stubResolver{err: errors.New("dead")}, &stubResolver{resp: aReply()}},
		"127.0.0.1", 0, nil, "Test", nil, h)
	if n := len(bundle.activeClients()); n != 2 {
		t.Fatalf("fail-open activeClients = %d, want 2", n)
	}
	got := bundle.Exchange(false, false)
	if got == nil || len(got.Answer) != 1 {
		t.Fatalf("fail-open should still produce an answer, got %v", got)
	}
}
