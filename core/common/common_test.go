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

func TestSetMinimumTTLCoversAllSections(t *testing.T) {
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	a, _ := dns.NewRR("example.com. 30 IN A 192.0.2.1")
	soa, _ := dns.NewRR("example.com. 30 IN SOA ns.example.com. hostmaster.example.com. 1 7200 900 1209600 60")
	msg.Answer = []dns.RR{a}
	msg.Ns = []dns.RR{soa}
	msg.Extra = []dns.RR{&dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}}

	SetMinimumTTL(msg, 3600)

	if got := msg.Answer[0].Header().Ttl; got != 3600 {
		t.Errorf("Answer TTL = %d, want 3600", got)
	}
	if got := msg.Ns[0].Header().Ttl; got != 3600 {
		t.Errorf("Ns SOA TTL = %d, want 3600 (authority must not bypass the minimum)", got)
	}
	if got := msg.Extra[0].Header().Ttl; got != 0 {
		t.Errorf("OPT TTL = %d, want untouched", got)
	}
}

func TestSetTTLByMapCoversAllSections(t *testing.T) {
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	a, _ := dns.NewRR("example.com. 300 IN A 192.0.2.1")
	cname, _ := dns.NewRR("www.example.com. 300 IN CNAME example.com.")
	soa, _ := dns.NewRR("example.com. 300 IN SOA ns.example.com. hostmaster.example.com. 1 7200 900 1209600 60")
	opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT, Ttl: 4096}}
	msg.Answer = []dns.RR{a, cname}
	msg.Ns = []dns.RR{soa}
	msg.Extra = []dns.RR{opt}

	SetTTLByMap(msg, map[string]uint32{"example.com": 60})

	if got := msg.Answer[0].Header().Ttl; got != 60 {
		t.Errorf("Answer TTL = %d, want 60", got)
	}
	if got := msg.Answer[1].Header().Ttl; got != 60 {
		t.Errorf("CNAME TTL = %d, want 60 (suffix match on subdomain)", got)
	}
	if got := msg.Ns[0].Header().Ttl; got != 60 {
		t.Errorf("Ns SOA TTL = %d, want 60 (authority section must be overridden)", got)
	}
	if got := msg.Extra[0].Header().Ttl; got != 4096 {
		t.Errorf("OPT TTL = %d, want untouched 4096", got)
	}
}

func TestSetTTLByMapNormalizesNameAndKeepsUnmatchedTTL(t *testing.T) {
	// domainTTLFile rules are regular expressions (see README); names are
	// normalized (trailing dot removed) before matching, so a regex anchored at
	// the end matches the bare name.
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	a, _ := dns.NewRR("example.com. 300 IN A 192.0.2.1")
	msg.Answer = []dns.RR{a}

	SetTTLByMap(msg, map[string]uint32{`example\.com$`: 30})
	if got := msg.Answer[0].Header().Ttl; got != 30 {
		t.Errorf("TTL = %d, want 30 (regex rule must match normalized name)", got)
	}

	// Unmatched names keep their original TTL.
	msg2 := new(dns.Msg)
	msg2.SetQuestion("other.org.", dns.TypeA)
	b, _ := dns.NewRR("other.org. 300 IN A 198.51.100.7")
	msg2.Answer = []dns.RR{b}
	SetTTLByMap(msg2, map[string]uint32{`example\.com$`: 30})
	if got := msg2.Answer[0].Header().Ttl; got != 300 {
		t.Errorf("TTL = %d, want untouched 300", got)
	}
}
