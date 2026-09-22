package clients

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
)

type captureResolver struct {
	got *dns.Msg
}

func (c *captureResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	c.got = q.Copy()
	resp := new(dns.Msg)
	resp.SetReply(q)
	return resp, nil
}
func (c *captureResolver) Init() error  { return nil }
func (c *captureResolver) Close() error { return nil }

func TestExchangeAdvertisesEDNS0WhenClientHasNone(t *testing.T) {
	up := &common.DNSUpstream{Name: "t", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	cap := &captureResolver{}
	c := NewClient(q, up, cap, "127.0.0.1", nil)
	c.Exchange(false)
	o := cap.got.IsEdns0()
	if o == nil {
		t.Fatal("outbound query carries no OPT record; upstream will truncate >512B answers")
	}
	if got := o.UDPSize(); got != 4096 {
		t.Fatalf("outbound EDNS0 buffer = %d, want 4096", got)
	}
}

func TestExchangePreservesClientEDNS0(t *testing.T) {
	up := &common.DNSUpstream{Name: "t", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.SetEdns0(1232, false)
	cap := &captureResolver{}
	c := NewClient(q, up, cap, "127.0.0.1", nil)
	c.Exchange(false)
	o := cap.got.IsEdns0()
	if o == nil {
		t.Fatal("client OPT was dropped")
	}
	if got := o.UDPSize(); got != 1232 {
		t.Fatalf("client EDNS0 buffer %d was overwritten, want 1232", got)
	}
}
