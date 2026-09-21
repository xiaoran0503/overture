package resolver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
)

func TestHTTPSResolverRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	resolver := &HTTPSResolver{BaseResolver: BaseResolver{dnsUpstream: &common.DNSUpstream{Address: server.URL}}, client: *server.Client()}
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	_, err := resolver.Exchange(query)
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %v, want HTTP status error", err)
	}
}

func TestHTTPSResolverRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(make([]byte, maxDNSMessageSize+1))
	}))
	defer server.Close()

	resolver := &HTTPSResolver{BaseResolver: BaseResolver{dnsUpstream: &common.DNSUpstream{Address: server.URL}}, client: *server.Client()}
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	_, err := resolver.Exchange(query)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want oversized response error", err)
	}
}
