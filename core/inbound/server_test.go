package inbound

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/cache"
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
