package common

import "testing"

// FuzzIsDomainMatchRule guards the shared regex rule evaluator used by the
// domain TTL map and hosts lookups: arbitrary patterns must be rejected or
// compiled with an error, never panic on the query path.
func FuzzIsDomainMatchRule(f *testing.F) {
	f.Add(`^www\.`, "www.example.com")
	f.Add("[", "example.com")
	f.Add("example.com", "www.example.com")
	f.Add(`(a|b)+`, "aaabbb")
	f.Add(`a{1000000}`, "a") // potential regexp resource pressure
	f.Add("", "example.com")
	f.Add(`\Qliteral\E`, "literal")
	f.Fuzz(func(t *testing.T, pattern, domain string) {
		_ = IsDomainMatchRule(pattern, domain) // must never panic
	})
}
