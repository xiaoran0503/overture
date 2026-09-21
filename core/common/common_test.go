package common

import (
	"net"
	"testing"

	"github.com/miekg/dns"
)

func TestReservedIPNetworkListCoversIPv6(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},
		{"10.1.2.3", true},
		{"172.16.5.5", true},
		{"192.168.1.1", true},
		{"100.64.0.9", true},
		{"8.8.8.8", false},
		{"114.114.114.114", false},
		{"::1", true},                   // IPv6 loopback
		{"::", true},                    // unspecified
		{"fd00::1", true},               // unique-local
		{"fe80::1", true},               // link-local
		{"2001:4860:4860::8888", false}, // public Google DNS
		{"2606:4700:4700::1111", false}, // public Cloudflare DNS
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("test IP %q did not parse", tc.ip)
		}
		if got := ReservedIPNetworkList.Contains(ip, false, ""); got != tc.want {
			t.Errorf("ReservedIPNetworkList.Contains(%q) = %v, want %v", tc.ip, got, tc.want)
		}
	}
}

func TestIsDomainMatchRuleCachesCompiledRegex(t *testing.T) {
	if !IsDomainMatchRule(`^www\.`, "www.example.com") {
		t.Fatal("expected pattern to match")
	}
	if IsDomainMatchRule(`^www\.`, "example.com") {
		t.Fatal("expected pattern not to match")
	}
	// A second call must reuse the cached compiled regex instead of erroring.
	if !IsDomainMatchRule(`^www\.`, "www.example.org") {
		t.Fatal("cached pattern failed to match on repeat use")
	}
	if IsDomainMatchRule(`[`, "example.com") {
		t.Fatal("invalid pattern should report no match")
	}
}

func TestSetEDNSClientSubnetRemovesCookies(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("example.com.", dns.TypeA)

	o := new(dns.OPT)
	o.SetUDPSize(4096)
	o.Hdr.Name = "."
	o.Hdr.Rrtype = dns.TypeOPT
	o.Option = append(o.Option, &dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "deadbeefdeadbeef"})
	o.Option = append(o.Option, &dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Address: net.ParseIP("0.0.0.0")})
	o.Option = append(o.Option, &dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "cafebabecafebabe"})
	m.Extra = append(m.Extra, o)

	SetEDNSClientSubnet(m, "192.0.2.1", true)

	opt := m.IsEdns0()
	if opt == nil {
		t.Fatal("EDNS OPT record was not kept")
	}
	for _, option := range opt.Option {
		if _, isCookie := option.(*dns.EDNS0_COOKIE); isCookie {
			t.Fatalf("cookie %v survived noCookie=true", option)
		}
	}
	subnet := IsEDNSClientSubnet(opt)
	if subnet == nil || subnet.Address.String() != "192.0.2.1" {
		t.Fatalf("EDNS client subnet = %v, want 192.0.2.1", subnet)
	}
}
