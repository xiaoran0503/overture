package common

import (
	"strings"
)

// DomainECSRule is one suffix-matching ECS override loaded from domainECSFile.
type DomainECSRule struct {
	Domain     string
	Policy     string
	ExternalIP string
	NoCookie   bool
}

// DomainECSMap is a list of ECS overrides. Lookup returns the longest suffix
// match so "www.example.com" prefers a "www.example.com" rule over "example.com".
type DomainECSMap []DomainECSRule

// Lookup returns an ECS policy for qname, or nil if no rule matches.
func (m DomainECSMap) Lookup(qname string) *EDNSClientSubnetType {
	if len(m) == 0 || qname == "" {
		return nil
	}
	qname = strings.ToLower(strings.TrimSuffix(qname, "."))
	bestLen := -1
	var best *DomainECSRule
	for i := range m {
		d := m[i].Domain
		if d == "" {
			continue
		}
		if !(HasSubDomain(d, qname) || d == qname) {
			continue
		}
		if len(d) > bestLen {
			best = &m[i]
			bestLen = len(d)
		}
	}
	if best == nil {
		return nil
	}
	return &EDNSClientSubnetType{
		Policy:     best.Policy,
		ExternalIP: best.ExternalIP,
		NoCookie:   best.NoCookie,
	}
}
