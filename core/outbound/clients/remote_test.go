package clients

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
)

type staticResolver struct{}

func (staticResolver) Init() error  { return nil }
func (staticResolver) Close() error { return nil }
func (staticResolver) Exchange(query *dns.Msg) (*dns.Msg, error) {
	response := new(dns.Msg)
	response.SetReply(query)
	return response, nil
}

func TestRemoteClientAllowsMissingEDNSConfig(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	client := NewClient(query, &common.DNSUpstream{Name: "test", Address: "127.0.0.1:53", Protocol: "udp"}, staticResolver{}, "", nil)
	if response := client.Exchange(false); response == nil {
		t.Fatal("expected response without EDNS configuration")
	}
}
