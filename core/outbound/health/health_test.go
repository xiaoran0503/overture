package health

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
)

type stubResolver struct {
	err   error
	resp  *dns.Msg
	calls atomic.Int32
}

func (s *stubResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	if s.resp == nil {
		m := new(dns.Msg)
		m.SetReply(q)
		return m, nil
	}
	r := s.resp.Copy()
	r.SetReply(q)
	return r, nil
}
func (s *stubResolver) Init() error  { return nil }
func (s *stubResolver) Close() error { return nil }

func testUp() *common.DNSUpstream {
	return &common.DNSUpstream{Name: "quad9", Address: "doq://dns.quad9.net:853", Protocol: "doq", Timeout: 3}
}

func TestNilCheckerIsFailOpen(t *testing.T) {
	var c *Checker
	if !c.Healthy(testUp()) {
		t.Fatal("nil checker must treat every upstream as healthy")
	}
	c.Record(testUp(), false)
	c.Start()
	c.Stop()
}

func TestDisabledReturnsNil(t *testing.T) {
	if New(Options{}, nil) != nil {
		t.Fatal("disabled checker should be nil")
	}
}

func TestConsecutiveFailuresMarkDown(t *testing.T) {
	u := testUp()
	c := New(Options{Enable: true, FailThreshold: 3, RecoverThreshold: 2}, []Target{{Group: "Primary", Upstream: u}})
	if !c.Healthy(u) {
		t.Fatal("new upstream should start healthy")
	}
	c.Record(u, false)
	c.Record(u, false)
	if !c.Healthy(u) {
		t.Fatal("below failThreshold should stay up")
	}
	c.Record(u, false)
	if c.Healthy(u) {
		t.Fatal("want down after failThreshold failures")
	}
}

func TestConsecutiveSuccessesRecover(t *testing.T) {
	u := testUp()
	c := New(Options{Enable: true, FailThreshold: 1, RecoverThreshold: 2}, []Target{{Group: "Primary", Upstream: u}})
	c.Record(u, false)
	if c.Healthy(u) {
		t.Fatal("want down after 1 failure")
	}
	c.Record(u, true)
	if c.Healthy(u) {
		t.Fatal("below recoverThreshold should stay down")
	}
	c.Record(u, true)
	if !c.Healthy(u) {
		t.Fatal("want up after recoverThreshold successes")
	}
}

func TestSuccessResetsFailCount(t *testing.T) {
	u := testUp()
	c := New(Options{Enable: true, FailThreshold: 3, RecoverThreshold: 1}, []Target{{Group: "Primary", Upstream: u}})
	c.Record(u, false)
	c.Record(u, false)
	c.Record(u, true)
	c.Record(u, false)
	if !c.Healthy(u) {
		t.Fatal("a success must reset the failure window")
	}
}

func TestProbeMarksDownAndRecover(t *testing.T) {
	u := testUp()
	stub := &stubResolver{err: errors.New("boom")}
	c := New(Options{Enable: true, Interval: 60, Timeout: 1, FailThreshold: 1, RecoverThreshold: 1, Domain: "example.com."},
		[]Target{{Group: "Primary", Upstream: u, Resolver: stub}})
	c.Start()
	defer c.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for c.Healthy(u) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c.Healthy(u) {
		t.Fatal("probe failure should mark the upstream down")
	}

	stub.err = nil
	c.probe(Target{Group: "Primary", Upstream: u, Resolver: stub})
	if !c.Healthy(u) {
		t.Fatal("successful probe should recover the upstream")
	}
}

func TestWithDefaults(t *testing.T) {
	o := (Options{Enable: true}).withDefaults()
	if o.Interval != 30 || o.Timeout != 5 || o.FailThreshold != 3 || o.RecoverThreshold != 2 || o.Domain != "example.com." {
		t.Fatalf("unexpected defaults: %+v", o)
	}
	if (Options{Domain: "example.com"}).withDefaults().Domain != "example.com." {
		t.Fatal("domain must gain a trailing dot")
	}
}
