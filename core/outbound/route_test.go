package outbound

import (
	"testing"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/matcher"
	matcherfull "github.com/shawn1m/overture/core/matcher/full"
)

func mapMatcher(names ...string) *matcherfull.Map {
	m := &matcherfull.Map{DataMap: make(map[string]struct{})}
	for _, n := range names {
		_ = m.Insert(n)
	}
	return m
}

func TestDecideDomainRoute(t *testing.T) {
	primary := mapMatcher("china.example")
	alt := mapMatcher("gfw.example", "china.example")
	empty := mapMatcher()

	cases := []struct {
		name  string
		qname string
		qtype uint16
		ipv6  bool
		p, a  matcher.Matcher
		want  domainDecision
	}{
		{"primary wins over alt", "china.example", dns.TypeA, false, primary, alt, decisionPrimaryDomain},
		{"alternative list", "gfw.example", dns.TypeA, false, primary, alt, decisionAlternativeDomain},
		{"aaaa ipv6 redirect", "other.example", dns.TypeAAAA, true, primary, alt, decisionIPv6},
		{"primary beats ipv6", "china.example", dns.TypeAAAA, true, primary, alt, decisionPrimaryDomain},
		{"undecided", "other.example", dns.TypeA, false, primary, alt, decisionUndecided},
		{"nil matchers", "example.com", dns.TypeA, false, nil, nil, decisionUndecided},
		{"empty matchers", "example.com", dns.TypeA, false, empty, empty, decisionUndecided},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideDomainRoute(tc.qname, tc.qtype, tc.ipv6, tc.p, tc.a)
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}
