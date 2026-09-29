package resolver

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
	"github.com/shawn1m/overture/core/common"
)

// startHTTPS3TestServer runs an HTTP/3 server on 127.0.0.1:0 serving /dns-query
// via the given handler.
func startHTTPS3TestServer(t *testing.T, handler http.HandlerFunc) (addr string, closeFn func()) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http3.Server{
		Handler:   handler,
		TLSConfig: doqTestTLSConfig(t),
	}
	go func() { _ = srv.Serve(pc) }()
	return pc.LocalAddr().String(), func() {
		_ = srv.Close()
		_ = pc.Close()
	}
}

func h3DoHHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dns-query" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, maxDNSMessageSize+1))
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		q := new(dns.Msg)
		if err := q.Unpack(data); err != nil {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		m := doqAnswer(q)
		out, err := m.Pack()
		if err != nil {
			http.Error(w, "pack failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	}
}

func newHTTPS3Resolver(t *testing.T, addr string, timeout int) *HTTPS3Resolver {
	t.Helper()
	r := &HTTPS3Resolver{BaseResolver: BaseResolver{dnsUpstream: &common.DNSUpstream{
		Name:     "test3",
		Address:  "https://" + addr + "/dns-query",
		Protocol: "https3",
		Timeout:  timeout,
	}}}
	// test-only: skip certificate verification against the self-signed server
	r.testTLSClientConfig = doqTestTLSConfig(t)
	r.testTLSClientConfig.InsecureSkipVerify = true
	return r
}

func TestHTTPS3ExchangeBasic(t *testing.T) {
	addr, closeFn := startHTTPS3TestServer(t, h3DoHHandler(t))
	defer closeFn()

	r := newHTTPS3Resolver(t, addr, 5)
	if err := r.Init(); err != nil {
		t.Fatalf("init failed: %v", err)
	}
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
}

func TestHTTPS3ExchangeHTTPError(t *testing.T) {
	addr, closeFn := startHTTPS3TestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	defer closeFn()

	r := newHTTPS3Resolver(t, addr, 5)
	if err := r.Init(); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	defer r.Close()

	if _, err := r.Exchange(doqQuery()); err == nil {
		t.Fatal("want HTTP error, got nil")
	}
}

func TestHTTPS3ExchangeLargeResponse(t *testing.T) {
	addr, closeFn := startHTTPS3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, maxDNSMessageSize+1))
		if err != nil {
			http.Error(w, "read failed", http.StatusBadRequest)
			return
		}
		q := new(dns.Msg)
		if err := q.Unpack(data); err != nil {
			http.Error(w, "bad message", http.StatusBadRequest)
			return
		}
		m := new(dns.Msg)
		m.SetReply(q)
		for i := 0; i < 60; i++ {
			rr, err := dns.NewRR("example.com. 60 IN TXT \"" + string(bytes.Repeat([]byte("x"), 200)) + "\"")
			if err != nil {
				http.Error(w, "rr failed", http.StatusInternalServerError)
				return
			}
			m.Answer = append(m.Answer, rr)
		}
		out, _ := m.Pack()
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(out)
	})
	defer closeFn()

	r := newHTTPS3Resolver(t, addr, 8)
	if err := r.Init(); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	defer r.Close()

	resp, err := r.Exchange(doqQuery())
	if err != nil {
		t.Fatalf("exchange failed: %v", err)
	}
	if len(resp.Answer) != 60 {
		t.Fatalf("want 60 answers, got %d", len(resp.Answer))
	}
}

func TestHTTPS3ExchangeTimeout(t *testing.T) {
	addr, closeFn := startHTTPS3TestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
	})
	defer closeFn()

	r := newHTTPS3Resolver(t, addr, 1)
	if err := r.Init(); err != nil {
		t.Fatalf("init failed: %v", err)
	}
	defer r.Close()

	start := time.Now()
	if _, err := r.Exchange(doqQuery()); err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took too long: %s", elapsed)
	}
}
