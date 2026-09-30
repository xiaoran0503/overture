package inbound

import (
	"bytes"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/cache"
	"github.com/shawn1m/overture/core/finder/full"
	"github.com/shawn1m/overture/core/hosts"
	"github.com/shawn1m/overture/core/outbound"
)

func TestIsLoopbackAddress(t *testing.T) {
	tests := []struct {
		address  string
		loopback bool
	}{
		{address: "127.0.0.1:5555", loopback: true},
		{address: "[::1]:5555", loopback: true},
		{address: ":5555", loopback: false},
		{address: "0.0.0.0:5555", loopback: false},
	}

	for _, tt := range tests {
		if got := isLoopbackAddress(tt.address); got != tt.loopback {
			t.Errorf("isLoopbackAddress(%q) = %v, want %v", tt.address, got, tt.loopback)
		}
	}
}

func TestHTTPTokenProtectsControlPaths(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "secret")
	s.HTTPMux.HandleFunc("/config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	unauthorized := httptest.NewRecorder()
	s.httpHandler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/config", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/config", nil)
	authorizedRequest.Header.Set("Authorization", "Bearer secret")
	authorized := httptest.NewRecorder()
	s.httpHandler().ServeHTTP(authorized, authorizedRequest)
	if authorized.Code != http.StatusNoContent {
		t.Fatalf("authorized status = %d, want %d", authorized.Code, http.StatusNoContent)
	}

	// /healthz and /metrics must be token-protected like every other debug path.
	for _, p := range []string{"/healthz", "/metrics"} {
		rec := httptest.NewRecorder()
		s.httpHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token status = %d, want 401", p, rec.Code)
		}
		rec2 := httptest.NewRecorder()
		r2 := httptest.NewRequest(http.MethodGet, p, nil)
		r2.Header.Set("Authorization", "Bearer secret")
		s.httpHandler().ServeHTTP(rec2, r2)
		if rec2.Code != http.StatusNotFound && rec2.Code != http.StatusOK {
			t.Fatalf("%s with token status = %d, want 404/200 (handler-dependent)", p, rec2.Code)
		}
	}
}

func TestRunRejectsUnprotectedRemoteHTTP(t *testing.T) {
	s := NewServer("127.0.0.1:53", "0.0.0.0:5555", outbound.Dispatcher{}, nil, false, "")
	if err := s.Run(); err == nil {
		t.Fatal("Run accepted an unprotected non-loopback debug HTTP address")
	}
}

func TestCheckBind(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	occupied := listener.Addr().String()
	defer listener.Close()

	if err := CheckBind(occupied, true); err == nil {
		t.Fatal("CheckBind accepted a TCP port that is already in use")
	}

	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freeAddress := free.Addr().String()
	free.Close()

	if err := CheckBind(freeAddress, true); err != nil {
		t.Fatalf("CheckBind rejected a free port: %s", err)
	}
}

func TestDumpCachePreservesMultiTokenRdata(t *testing.T) {
	cache := cache.New(4, "", 0)
	message := new(dns.Msg)
	message.SetQuestion("example.com.", dns.TypeTXT)
	txt, _ := dns.NewRR(`example.com. 60 IN TXT "first" "second with spaces"`)
	message.Answer = []dns.RR{txt}
	cache.InsertMessageToLocal("example.com. 16", message, 60)

	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{Cache: cache}, nil, false, "")
	req := httptest.NewRequest(http.MethodGet, "/cache?nobody=false", nil)
	rec := httptest.NewRecorder()
	s.DumpCache(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "first") || !strings.Contains(body, "second with spaces") {
		t.Fatalf("cache dump truncated multi-token rdata: %s", body)
	}
}

func TestRunReturnsBindErrorInsteadOfExiting(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	occupied := listener.Addr().String()
	packet, err := net.ListenPacket("udp", occupied)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	defer packet.Close()

	s := NewServer(occupied, "", outbound.Dispatcher{}, nil, false, "")
	defer s.cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- s.Run() }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil error while the port is occupied")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after bind failures")
	}
}

func TestStopWaitsForRunToFinish(t *testing.T) {
	s := NewServer("127.0.0.1:0", "", outbound.Dispatcher{}, nil, false, "")
	go s.Run()
	select {
	case <-s.started:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not start")
	}

	stopped := make(chan struct{})
	go func() { s.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return after Run finished")
	}
}

// mockResponseWriter captures the message written by ServeDNS.
type mockResponseWriter struct {
	msg *dns.Msg
}

func (m *mockResponseWriter) WriteMsg(msg *dns.Msg) error { m.msg = msg; return nil }
func (m *mockResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (m *mockResponseWriter) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 53}
}
func (m *mockResponseWriter) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 55555}
}
func (m *mockResponseWriter) Close() error           { return nil }
func (m *mockResponseWriter) TsigStatus() error      { return nil }
func (m *mockResponseWriter) TsigTimersOnly(bool)    {}
func (m *mockResponseWriter) Hijack()                {}
func (m *mockResponseWriter) SetTsigStatus(error)    {}
func (m *mockResponseWriter) SetTsigTimersOnly(bool) {}

func TestServeDNSCompressesResponses(t *testing.T) {
	hostsFile, err := os.CreateTemp("", "overture-hosts-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hostsFile.Name())
	if _, err := hostsFile.WriteString("1.2.3.4 example.com.\n"); err != nil {
		t.Fatal(err)
	}
	hostsFile.Close()
	h, err := hosts.New(hostsFile.Name(), &full.Map{DataMap: make(map[string][]string)})
	if err != nil {
		t.Fatal(err)
	}

	s := NewServer("127.0.0.1:53", "", outbound.Dispatcher{Hosts: h}, nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil {
		t.Fatal("ServeDNS wrote no response")
	}
	// miekg's Unpack clears Compress; the live write-back path must re-enable
	// it, otherwise large responses leave overture ~1.6x larger than needed
	// and can outgrow the client's EDNS0 buffer.
	if !w.msg.Compress {
		t.Fatal("response was written without name compression")
	}
}

func TestServeDNSHttpRejectsNotifyOpcode(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Opcode = dns.OpcodeNotify
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (NOTIMP)", rec.Code)
	}
}

func TestServeDNSHttpRejectsEmptyQuestion(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")

	// A 12-byte DNS header with QDCOUNT = 0 bypasses the UDP/TCP accept path
	// (DoH unpacks directly) and used to panic on q.Question[0].
	body := make([]byte, 12)
	body[2] = 0x01 // RD
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no panic)", rec.Code)
	}
}

func TestServeDNSRejectsNonQueryOpcode(t *testing.T) {
	s := NewServer("127.0.0.1:53", "", outbound.Dispatcher{}, nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	q.Opcode = dns.OpcodeNotify
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil || w.msg.Rcode != dns.RcodeNotImplemented {
		t.Fatalf("ServeDNS(NOTIFY) rcode = %v, want NOTIMP", w.msg)
	}
}

func TestServeDNSRejectsConfiguredType(t *testing.T) {
	s := NewServer("127.0.0.1:53", "", outbound.Dispatcher{}, []uint16{dns.TypeANY}, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeANY)
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("ServeDNS(reject ANY) rcode = %v, want SERVFAIL", w.msg)
	}
}

func TestServeDNSReturnsFailedWhenDispatcherNil(t *testing.T) {
	s := NewServer("127.0.0.1:53", "", outbound.Dispatcher{}, nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil || w.msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("ServeDNS(empty dispatcher) rcode = %v, want SERVFAIL", w.msg)
	}
}

func TestIsQuestionType(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	if !isQuestionType(q, dns.TypeA) {
		t.Fatal("isQuestionType(A) = false, want true")
	}
	if isQuestionType(q, dns.TypeAAAA) {
		t.Fatal("isQuestionType(AAAA) = true, want false")
	}
}

func hostDispatcher(t *testing.T) outbound.Dispatcher {
	t.Helper()
	hostsFile, err := os.CreateTemp("", "overture-hosts-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(hostsFile.Name())
	if _, err := hostsFile.WriteString("1.2.3.4 example.com.\n"); err != nil {
		t.Fatal(err)
	}
	hostsFile.Close()
	h, err := hosts.New(hostsFile.Name(), &full.Map{DataMap: make(map[string][]string)})
	if err != nil {
		t.Fatal(err)
	}
	return outbound.Dispatcher{Hosts: h}
}

func TestServeDNSHttpPostNormalQuery(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", hostDispatcher(t), nil, false, "")
	s.registerDebugHandlers()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(rec.Body.Bytes()); err != nil {
		t.Fatalf("response does not unpack: %v", err)
	}
	if len(resp.Answer) != 1 || resp.Answer[0].(*dns.A).A.String() != "1.2.3.4" {
		t.Fatalf("response answers = %v, want 1.2.3.4", resp.Answer)
	}
}

func TestServeDNSHttpGetMode(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", hostDispatcher(t), nil, false, "")
	s.registerDebugHandlers()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/dns-query?dns="+base64.RawURLEncoding.EncodeToString(body), nil)
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rec.Code)
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(rec.Body.Bytes()); err != nil {
		t.Fatalf("GET response does not unpack: %v", err)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("GET response answers = %d, want 1", len(resp.Answer))
	}
}

func TestServeDNSHttpWrongPathIs404(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")
	req := httptest.NewRequest(http.MethodGet, "/other", nil)
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestServeDNSHttpRejectsConfiguredType(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, []uint16{dns.TypeANY}, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeANY)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServeDNSHttpNoResponseIs500(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestDebugHTTPHealthz(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")
	s.registerDebugHandlers()
	rec := httptest.NewRecorder()
	s.HTTPMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "ok\n" {
		t.Fatalf("/healthz = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestMetricsEndpointReflectsQueries(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", hostDispatcher(t), nil, false, "")
	s.registerDebugHandlers()
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil {
		t.Fatal("ServeDNS wrote no response")
	}

	rec := httptest.NewRecorder()
	s.HTTPMux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "overture_dns_queries_total") {
		t.Fatalf("/metrics missing query counter:\n%s", body)
	}
	if !strings.Contains(body, "overture_dns_responses_total") {
		t.Fatalf("/metrics missing response counter:\n%s", body)
	}
	if !strings.Contains(body, "overture_up") {
		t.Fatalf("/metrics missing liveness gauge:\n%s", body)
	}
}

func TestServeDNSRejectsEmptyQuestion(t *testing.T) {
	s := NewServer("127.0.0.1:53", "", outbound.Dispatcher{}, nil, false, "")
	q := new(dns.Msg)
	q.Id = 42
	q.RecursionDesired = true
	w := &mockResponseWriter{}
	s.ServeDNS(w, q)
	if w.msg == nil || w.msg.Rcode != dns.RcodeFormatError {
		t.Fatalf("ServeDNS(empty question) rcode = %v, want FORMERR", w.msg)
	}
}

func TestServeDNSHttpMethodNotAllowed(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")
	req := httptest.NewRequest(http.MethodPut, "/dns-query", nil)
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if rec.Header().Get("Allow") != "GET, POST" {
		t.Fatalf("Allow = %q, want GET, POST", rec.Header().Get("Allow"))
	}
}

func TestServeDNSHttpPostWrongContentType(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", outbound.Dispatcher{}, nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestServeDNSHttpCacheControlIntegerMaxAge(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", hostDispatcher(t), nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !regexp.MustCompile(`^max-age=\d+$`).MatchString(cc) {
		t.Fatalf("Cache-Control = %q, want integer max-age", cc)
	}
}

func TestDohCacheControl(t *testing.T) {
	ok := new(dns.Msg)
	ok.SetQuestion("example.com.", dns.TypeA)
	ok.Response = true
	rr, err := dns.NewRR("example.com. 60 IN A 1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	ok.Answer = []dns.RR{rr}
	if got := dohCacheControl(ok); got != "max-age=60" {
		t.Fatalf("NOERROR Cache-Control = %q, want max-age=60", got)
	}

	fail := new(dns.Msg)
	fail.SetQuestion("example.com.", dns.TypeA)
	fail.SetRcode(fail, dns.RcodeServerFailure)
	if got := dohCacheControl(fail); got != "no-store" {
		t.Fatalf("SERVFAIL Cache-Control = %q, want no-store", got)
	}

	nx := new(dns.Msg)
	nx.SetQuestion("missing.example.com.", dns.TypeA)
	nx.SetRcode(nx, dns.RcodeNameError)
	soa, err := dns.NewRR("example.com. 300 IN SOA ns.example.com. a.example.com. 1 1 1 1 90")
	if err != nil {
		t.Fatal(err)
	}
	nx.Ns = []dns.RR{soa}
	got := dohCacheControl(nx)
	if !regexp.MustCompile(`^max-age=\d+$`).MatchString(got) {
		t.Fatalf("NXDOMAIN Cache-Control = %q, want integer max-age", got)
	}
}

func TestIsDNSMessageContentType(t *testing.T) {
	if !isDNSMessageContentType("application/dns-message") {
		t.Fatal("plain mime should be accepted")
	}
	if !isDNSMessageContentType("application/dns-message; charset=utf-8") {
		t.Fatal("mime with parameter should be accepted")
	}
	if isDNSMessageContentType("") || isDNSMessageContentType("text/plain") {
		t.Fatal("empty or wrong mime should be rejected")
	}
}
