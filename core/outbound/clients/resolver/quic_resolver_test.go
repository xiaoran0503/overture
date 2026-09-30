package resolver

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
	"github.com/shawn1m/overture/core/common"
)

// --- test-only DoQ server ---

func doqTestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "doq.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"doq.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{doqALPN}, MinVersion: tls.VersionTLS13}
}

// startDoQTestServer listens on 127.0.0.1:0 and serves one stream per query.
// handler returns the response message, or nil to drop the stream.
func startDoQTestServer(t *testing.T, handler func(*dns.Msg) *dns.Msg) (addr string, closeFn func()) {
	t.Helper()
	listener, err := quic.ListenAddr("127.0.0.1:0", doqTestTLSConfig(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go serveDoQTestConn(conn, handler)
		}
	}()
	return listener.Addr().String(), func() { _ = listener.Close() }
}

func serveDoQTestConn(conn *quic.Conn, handler func(*dns.Msg) *dns.Msg) {
	defer conn.CloseWithError(0, "done")
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func(s *quic.Stream) {
			defer s.Close()
			serveDoQTestStream(s, handler)
		}(stream)
	}
}

func serveDoQTestStream(s *quic.Stream, handler func(*dns.Msg) *dns.Msg) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		return
	}
	length := binary.BigEndian.Uint16(lenBuf[:])
	if length == 0 {
		return
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(s, data); err != nil {
		return
	}
	q := new(dns.Msg)
	if err := q.Unpack(data); err != nil {
		return
	}
	resp := handler(q)
	if resp == nil {
		return
	}
	out, err := resp.Pack()
	if err != nil {
		return
	}
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(out)))
	_, _ = s.Write(lenBuf[:])
	_, _ = s.Write(out)
}

func doqAnswer(q *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	rr, _ := dns.NewRR("example.com. 60 IN A 192.0.2.1")
	m.Answer = []dns.RR{rr}
	return m
}

func newQUICResolver(t *testing.T, addr string, timeout int) *QUICResolver {
	t.Helper()
	r := &QUICResolver{BaseResolver: BaseResolver{dnsUpstream: &common.DNSUpstream{
		Name:     "test",
		Address:  "doq://doq.test@" + addr,
		Protocol: "doq",
		Timeout:  timeout,
	}}}
	// test-only: skip certificate verification against the self-signed server
	r.testTLSConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{doqALPN}, MinVersion: tls.VersionTLS13}
	return r
}

func doqQuery() *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	return q
}

func TestQUICExchangeBasic(t *testing.T) {
	addr, closeFn := startDoQTestServer(t, doqAnswer)
	defer closeFn()

	r := newQUICResolver(t, addr, 5)
	defer r.Close()

	q := doqQuery()
	resp, err := r.Exchange(q)
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("want 1 answer, got %d", len(resp.Answer))
	}
	if resp.Answer[0].Header().Name != "example.com." {
		t.Fatalf("unexpected answer name: %s", resp.Answer[0].Header().Name)
	}
	if resp.Id != q.Id {
		t.Fatalf("response ID %d != query ID %d", resp.Id, q.Id)
	}
}

func TestQUICExchangeIDMismatchRejected(t *testing.T) {
	addr, closeFn := startDoQTestServer(t, func(q *dns.Msg) *dns.Msg {
		m := doqAnswer(q)
		m.Id = q.Id + 1
		return m
	})
	defer closeFn()

	r := newQUICResolver(t, addr, 5)
	defer r.Close()

	if _, err := r.Exchange(doqQuery()); err == nil {
		t.Fatal("want ID mismatch error, got nil")
	}
}

func TestQUICExchangeReconnectAfterConnDrop(t *testing.T) {
	addr, closeFn := startDoQTestServer(t, doqAnswer)
	defer closeFn()

	r := newQUICResolver(t, addr, 5)
	defer r.Close()

	// First exchange establishes the connection.
	resp, err := r.Exchange(doqQuery())
	if err != nil || len(resp.Answer) != 1 {
		t.Fatalf("first exchange failed: %v", err)
	}

	// Kill the cached connection from the client side, then the next exchange
	// must transparently reconnect and succeed.
	r.mu.Lock()
	if r.conn != nil {
		_ = r.conn.CloseWithError(0, "simulated drop")
	}
	r.mu.Unlock()

	resp, err = r.Exchange(doqQuery())
	if err != nil || len(resp.Answer) != 1 {
		t.Fatalf("exchange after drop failed: %v", err)
	}
}

func TestQUICExchangeTimeout(t *testing.T) {
	addr, closeFn := startDoQTestServer(t, func(*dns.Msg) *dns.Msg {
		// Swallow the query: never respond.
		time.Sleep(5 * time.Second)
		return nil
	})
	defer closeFn()

	r := newQUICResolver(t, addr, 1)
	defer r.Close()

	start := time.Now()
	if _, err := r.Exchange(doqQuery()); err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}

func TestParseDoQAddress(t *testing.T) {
	cases := []struct {
		in      string
		host    string
		port    string
		sni     string
		wantErr bool
	}{
		{in: "dns.adguard-dns.com:853", host: "dns.adguard-dns.com", port: "853", sni: "dns.adguard-dns.com"},
		{in: "doq://dns.adguard-dns.com", host: "dns.adguard-dns.com", port: "853", sni: "dns.adguard-dns.com"},
		{in: "quic://dns.adguard-dns.com:8853", host: "dns.adguard-dns.com", port: "8853", sni: "dns.adguard-dns.com"},
		{in: "doq://dns.alidns.com@223.5.5.5:853", host: "223.5.5.5", port: "853", sni: "dns.alidns.com"},
		{in: "223.5.5.5:853", wantErr: true},
	}
	for _, c := range cases {
		host, port, sni, err := parseDoQAddress(c.in)
		if c.wantErr {
			if err == nil {
				t.Fatalf("parseDoQAddress(%q) want error, got %s:%s sni=%s", c.in, host, port, sni)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseDoQAddress(%q) failed: %v", c.in, err)
		}
		if host != c.host || port != c.port || sni != c.sni {
			t.Fatalf("parseDoQAddress(%q) = %s:%s sni=%s, want %s:%s sni=%s", c.in, host, port, sni, c.host, c.port, c.sni)
		}
	}
}

func TestQUICLiveQuad9(t *testing.T) {
	if os.Getenv("OVERTURE_LIVE_DOQ") == "" {
		t.Skip("set OVERTURE_LIVE_DOQ=1 to smoke-test doq://dns.quad9.net")
	}
	r := NewResolver(&common.DNSUpstream{
		Name:     "Quad9",
		Address:  "doq://dns.quad9.net:853",
		Protocol: "doq",
		Timeout:  8,
	})
	defer r.Close()
	for _, name := range []string{"example.com.", "www.ietf.org."} {
		q := new(dns.Msg)
		q.SetQuestion(name, dns.TypeA)
		resp, err := r.Exchange(q)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if resp == nil || len(resp.Answer) == 0 {
			t.Fatalf("%s: empty answer: %v", name, resp)
		}
		t.Logf("%s answers=%d", name, len(resp.Answer))
	}
}
