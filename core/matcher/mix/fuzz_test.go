package mix

import "testing"

// FuzzMixListInsertHas guards the matcher hot path: rule insertion must never
// accept input that later panics in Has (invalid regex rules were a remote DoS
// in v2.0.2, fixed in v2.0.3 by validating at Insert time).
func FuzzMixListInsertHas(f *testing.F) {
	f.Add("domain:example.com", "www.example.com")
	f.Add("regex:^www\\.", "www.example.com")
	f.Add("regex:foo(", "www.example.com")
	f.Add("regex:https?://", "www.example.com")
	f.Add("keyword:example", "example.org")
	f.Add("full:exact.com", "exact.com")
	f.Add("example.com", "a.example.com")
	f.Add("", "")
	f.Fuzz(func(t *testing.T, rule, domain string) {
		l := new(List)
		if err := l.Insert(rule); err != nil {
			return // invalid rules are rejected at insert time
		}
		_ = l.Has(domain) // must never panic
	})
}
