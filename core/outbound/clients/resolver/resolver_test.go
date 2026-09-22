package resolver

import (
	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
	"net"
	"os"
	"testing"
	"time"
)

var questionDomain = "www.yahoo.com."
var udpUpstream = &common.DNSUpstream{
	Name:          "Test-UDP",
	Address:       "114.114.114.114",
	Protocol:      "udp",
	SOCKS5Address: "",
	Timeout:       6,
	EDNSClientSubnet: &common.EDNSClientSubnetType{
		Policy:     "disable",
		ExternalIP: "",
		NoCookie:   false,
	},
}

var tcpUpstream = &common.DNSUpstream{
	Name:          "Test-TCP",
	Address:       "1.1.1.1",
	Protocol:      "tcp",
	SOCKS5Address: "",
	Timeout:       6,
	EDNSClientSubnet: &common.EDNSClientSubnetType{
		Policy:     "disable",
		ExternalIP: "",
		NoCookie:   false,
	},
}

var tcpTlsUpstream = &common.DNSUpstream{
	Name:          "Test-TCP-TLS",
	Address:       "dns.google@8.8.8.8",
	Protocol:      "tcp-tls",
	SOCKS5Address: "",
	Timeout:       8,
	EDNSClientSubnet: &common.EDNSClientSubnetType{
		Policy:     "disable",
		ExternalIP: "",
		NoCookie:   false,
	},
}

var httpsUpstream = &common.DNSUpstream{
	Name:          "Test-HTTPS",
	Address:       "https://dns.google/dns-query",
	Protocol:      "https",
	SOCKS5Address: "",
	Timeout:       8,
	EDNSClientSubnet: &common.EDNSClientSubnetType{
		Policy:     "disable",
		ExternalIP: "",
		NoCookie:   false,
	},
}

func init() {
	os.Chdir("../..")
}

func TestDispatcher(t *testing.T) {
	if os.Getenv("OVERTURE_NETWORK_TESTS") != "1" {
		t.Skip("set OVERTURE_NETWORK_TESTS=1 to run external DNS integration tests")
	}
	testUDP(t)
	testTCP(t)
	testTCPTLS(t)
	testHTTPS(t)
}

func testUDP(t *testing.T) {
	q := getQueryMsg(questionDomain, dns.TypeA)
	resolver := NewResolver(udpUpstream)
	resp, err := resolver.Exchange(q)
	if err != nil {
		t.Errorf("Got error: %s", err)
	}
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeA)).To4() == nil {
		t.Error(questionDomain + " should have A record")
	}
}

func testTCP(t *testing.T) {
	q := getQueryMsg(questionDomain, dns.TypeA)
	resolver := NewResolver(tcpUpstream)
	resp, _ := resolver.Exchange(q)
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeA)).To4() == nil {
		t.Error(questionDomain + " should have A record")
	}
}

func testTCPTLS(t *testing.T) {
	q := getQueryMsg(questionDomain, dns.TypeA)
	resolver := NewResolver(tcpTlsUpstream)
	resp, _ := resolver.Exchange(q)
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeA)).To4() == nil {
		t.Error(questionDomain + " should have A record")
	}
}

func testHTTPS(t *testing.T) {
	q := getQueryMsg(questionDomain, dns.TypeA)
	resolver := NewResolver(httpsUpstream)
	resp, _ := resolver.Exchange(q)
	if net.ParseIP(common.FindRecordByType(resp, dns.TypeA)).To4() == nil {
		t.Error(questionDomain + " should have A record")
	}
}

func getQueryMsg(z string, t uint16) *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion(z, t)
	return q
}

// mockConn returns a fixed payload on every Read, enough for the UDP-style
// single-read path used by dns.Conn with UDPSize set.
type mockConn struct {
	response []byte
}

func (m *mockConn) Read(b []byte) (int, error) {
	return copy(b, m.response), nil
}

func (m *mockConn) Write(b []byte) (int, error) { return len(b), nil }

func (m *mockConn) Close() error { return nil }

func (m *mockConn) LocalAddr() net.Addr  { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }
func (m *mockConn) RemoteAddr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func (m *mockConn) SetDeadline(time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(time.Time) error { return nil }

func TestExchangeRejectsMismatchedResponseID(t *testing.T) {
	r := &BaseResolver{dnsUpstream: &common.DNSUpstream{Name: "test", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}}

	// SetQuestion resets Id to a random value, so assign the ID afterwards.
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Id = 0x1234

	// A well-formed response carrying a different transaction ID must be
	// rejected so a spoofed or cross-talk UDP packet is never accepted.
	resp := new(dns.Msg)
	resp.SetQuestion("example.com.", dns.TypeA)
	resp.Id = 0x9999
	resp.Response = true
	packed, err := resp.Pack()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := r.exchangeByConnWithoutClose(q, &mockConn{response: packed}); err == nil {
		t.Fatal("exchange accepted a response with a mismatched ID")
	}
}

func TestExchangeAcceptsMatchingResponseID(t *testing.T) {
	r := &BaseResolver{dnsUpstream: &common.DNSUpstream{Name: "test", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}}

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Id = 0x1234

	resp := new(dns.Msg)
	resp.SetQuestion("example.com.", dns.TypeA)
	resp.Id = q.Id
	resp.Response = true
	packed, err := resp.Pack()
	if err != nil {
		t.Fatal(err)
	}

	got, err := r.exchangeByConnWithoutClose(q, &mockConn{response: packed})
	if err != nil {
		t.Fatalf("matching-ID response was rejected: %s", err)
	}
	if got.Id != q.Id {
		t.Fatalf("response ID = %d, want %d", got.Id, q.Id)
	}
}
