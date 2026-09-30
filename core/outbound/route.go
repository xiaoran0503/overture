package outbound

import (
	"strings"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/matcher"
	"github.com/shawn1m/overture/core/metrics"
	log "github.com/sirupsen/logrus"
)

// domainDecision is the domain-table / AAAA-redirect outcome, independent of
// IP-network classification. IP-network is never cached.
type domainDecision int

const (
	decisionPrimaryDomain domainDecision = iota
	decisionAlternativeDomain
	decisionIPv6
	decisionUndecided
)

const (
	routeCachePrimary     = "Primary"
	routeCacheAlternative = "Alternative"
	routeCacheUndecided   = "undecided"

	routeReasonDomainPrimary     = "domain_primary"
	routeReasonDomainAlternative = "domain_alternative"
	routeReasonIPv6              = "ipv6"
	routeReasonIPNet             = "ipnet"
)

func (d domainDecision) String() string {
	switch d {
	case decisionPrimaryDomain:
		return "domain_primary"
	case decisionAlternativeDomain:
		return "domain_alternative"
	case decisionIPv6:
		return "ipv6"
	default:
		return "undecided"
	}
}

// decideDomainRoute is the pure domain-routing function. Order matches the
// historical dispatcher: primary list, then AAAA redirect, then alternative
// list, else undecided (caller then does IP-network classify).
func decideDomainRoute(qname string, qtype uint16, ipv6Redirect bool, primary, alt matcher.Matcher) domainDecision {
	if matcherHas(primary, qname) {
		log.Debugf("Finally use Primary DNS")
		return decisionPrimaryDomain
	}
	if qtype == dns.TypeAAAA && ipv6Redirect {
		log.Debug("Finally use alternative DNS")
		return decisionIPv6
	}
	if matcherHas(alt, qname) {
		log.Debugf("Finally use Alternative DNS")
		return decisionAlternativeDomain
	}
	return decisionUndecided
}

func matcherHas(m matcher.Matcher, qname string) bool {
	if m == nil {
		return false
	}
	return m.Has(qname)
}

func recordRoute(reason string) {
	metrics.DNSRouteTotal.WithLabelValues(reason).Inc()
}

func questionDomain(q *dns.Msg) string {
	if q == nil || len(q.Question) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(q.Question[0].Name, "."))
}
