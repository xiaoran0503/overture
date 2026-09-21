package outbound

import (
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/config"
)

var dispatcher Dispatcher
var questionDomain = "www.yahoo.com."

func init() {
	os.Chdir("../..")
	conf := config.NewConfig("config.test.yml")
	dispatcher = Dispatcher{
		PrimaryDNS:                  conf.PrimaryDNS,
		AlternativeDNS:              conf.AlternativeDNS,
		OnlyPrimaryDNS:              conf.OnlyPrimaryDNS,
		WhenPrimaryDNSAnswerNoneUse: conf.WhenPrimaryDNSAnswerNoneUse,
		IPNetworkPrimarySet:         conf.IPNetworkPrimarySet,
		IPNetworkAlternativeSet:     conf.IPNetworkAlternativeSet,
		DomainPrimaryList:           conf.DomainPrimaryList,
		DomainAlternativeList:       conf.DomainAlternativeList,

		RedirectIPv6Record:       conf.IPv6UseAlternativeDNS,
		AlternativeDNSConcurrent: conf.AlternativeDNSConcurrent,
		MinimumTTL:               conf.MinimumTTL,
		DomainTTLMap:             conf.DomainTTLMap,

		Hosts: conf.Hosts,
		Cache: conf.Cache,
	}
	dispatcher.Init()
}

func TestDispatcher(t *testing.T) {
	testHosts(t)
	testIPResponse(t)
	if os.Getenv("OVERTURE_NETWORK_TESTS") != "1" {
		t.Log("set OVERTURE_NETWORK_TESTS=1 to run external DNS integration tests")
		return
	}
	testA(t)
	testAAAA(t)
	testCache(t)
}

func testA(t *testing.T) {

	resp := exchange(questionDomain, dns.TypeA)
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeA)).To4() == nil {
		t.Error(questionDomain + " should have A record")
	}
}

func testAAAA(t *testing.T) {

	resp := exchange(questionDomain, dns.TypeAAAA)
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeAAAA)).To16() == nil {
		t.Error(questionDomain + " should have AAAA record")
	}
}

func testHosts(t *testing.T) {

	resp := exchange("localhost.", dns.TypeA)
	if common.FindRecordByType(resp, dns.TypeA) != "127.0.0.1" {
		t.Error("localhost should be 127.0.0.1")
	}
}

func testIPResponse(t *testing.T) {

	resp := exchange("127.0.0.1.", dns.TypeA)
	if common.FindRecordByType(resp, dns.TypeA) != "127.0.0.1" {
		t.Error("127.0.0.1 should be 127.0.0.1")
	}

	resp = exchange("fe80::7f:4f42:3f4d:f4c8.", dns.TypeAAAA)
	if common.FindRecordByType(resp, dns.TypeAAAA) != "fe80::7f:4f42:3f4d:f4c8" {
		t.Error("fe80::7f:4f42:3f4d:f4c8 should be fe80::7f:4f42:3f4d:f4c8")
	}
}

func testCache(t *testing.T) {

	exchange(questionDomain, dns.TypeA)
	now := time.Now()
	exchange(questionDomain, dns.TypeA)
	if time.Since(now) > 10*time.Millisecond {
		t.Error(time.Since(now).String() + " " + "Cache response slower than 10ms")
	}
}

func exchange(z string, t uint16) *dns.Msg {

	q := new(dns.Msg)
	q.SetQuestion(z, t)
	return dispatcher.Exchange(q, "")
}

func TestConcurrentAlternativeDoesNotLeak(t *testing.T) {
	primaryAddress, stopPrimary := startTestDNSServer(t, 0)
	defer stopPrimary()
	alternativeAddress, stopAlternative := startTestDNSServer(t, 30*time.Millisecond)
	defer stopAlternative()
	_, allIPv4, _ := net.ParseCIDR("0.0.0.0/0")
	d := Dispatcher{
		PrimaryDNS:                  []*common.DNSUpstream{{Name: "primary", Address: primaryAddress, Protocol: "udp", Timeout: 3}},
		AlternativeDNS:              []*common.DNSUpstream{{Name: "alternative", Address: alternativeAddress, Protocol: "udp", Timeout: 3}},
		AlternativeDNSConcurrent:    true,
		IPNetworkPrimarySet:         common.NewIPSet([]*net.IPNet{allIPv4}),
		WhenPrimaryDNSAnswerNoneUse: "primaryDNS",
	}
	d.Init()
	baseline := runtime.NumGoroutine()
	for i := 0; i < 30; i++ {
		query := new(dns.Msg)
		query.SetQuestion("example.com.", dns.TypeA)
		if response := d.Exchange(query, ""); response == nil {
			t.Fatal("primary DNS response was nil")
		}
	}
	time.Sleep(100 * time.Millisecond)
	if leaked := runtime.NumGoroutine() - baseline; leaked > 8 {
		t.Fatalf("concurrent alternative leaked %d goroutines", leaked)
	}
	d.Close()
}

func startTestDNSServer(t *testing.T, delay time.Duration) (string, func()) {
	t.Helper()
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: listener, Handler: dns.HandlerFunc(func(writer dns.ResponseWriter, request *dns.Msg) {
		if delay > 0 {
			time.Sleep(delay)
		}
		response := new(dns.Msg)
		response.SetReply(request)
		record, _ := dns.NewRR("example.com. 60 IN A 192.0.2.1")
		response.Answer = append(response.Answer, record)
		_ = writer.WriteMsg(response)
	})}
	go func() { _ = server.ActivateAndServe() }()
	return listener.LocalAddr().String(), func() { _ = server.Shutdown() }
}
