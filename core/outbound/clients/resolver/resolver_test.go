package resolver

import (
	"fmt"

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

func TestUDPResolverFallsBackToTCP(t *testing.T) {
	// Reserve one port and serve both UDP and TCP fake upstreams on it, as a
	// real upstream does (the TCP fallback dials the same address).
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpLn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer udpLn.Close()
	port := udpLn.LocalAddr().(*net.UDPAddr).Port
	tcpLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()
	tcpQueries := make(chan int, 1)
	go func() {
		conn, err := tcpLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dc := &dns.Conn{Conn: conn}
		msg, err := dc.ReadMsg()
		if err != nil {
			return
		}
		tcpQueries <- 1
		full := new(dns.Msg)
		full.SetReply(msg)
		for i := 0; i < 60; i++ {
			rr, _ := dns.NewRR("www.example.com. 300 IN A 5.6.7." + fmt.Sprint(i%254))
			full.Answer = append(full.Answer, rr)
		}
		_ = dc.WriteMsg(full)
	}()

	// Fake UDP upstream: always truncates (TC=1) and sends 30 of 60 answers.
	go func() {
		// The UDP listener is unconnected, so reply with WriteToUDP and the
		// observed source address (dns.Conn.WriteMsg would fail with
		// "destination address required").
		buf := make([]byte, 4096)
		n, addr, err := udpLn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msg := new(dns.Msg)
		if err := msg.Unpack(buf[:n]); err != nil {
			return
		}
		trunc := new(dns.Msg)
		trunc.SetReply(msg)
		trunc.Truncated = true
		for i := 0; i < 30; i++ {
			rr, _ := dns.NewRR("www.example.com. 300 IN A 5.6.7." + fmt.Sprint(i%254))
			trunc.Answer = append(trunc.Answer, rr)
		}
		out, err := trunc.Pack()
		if err != nil {
			return
		}
		_, _ = udpLn.WriteToUDP(out, addr)
	}()

	u := &common.DNSUpstream{Name: "fallback", Address: udpLn.LocalAddr().String(), Protocol: "udp", Timeout: 3}
	q := new(dns.Msg)
	q.SetQuestion("www.example.com.", dns.TypeA)
	resp, err := NewResolver(u).Exchange(q)
	if err != nil {
		t.Fatalf("exchange failed: %s", err)
	}
	if resp.Truncated {
		t.Fatal("TCP fallback still returned a truncated response")
	}
	if len(resp.Answer) != 60 {
		t.Fatalf("TCP fallback returned %d answers, want 60", len(resp.Answer))
	}
	select {
	case <-tcpQueries:
	default:
		t.Fatal("upstream TCP server was never queried")
	}
}

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
