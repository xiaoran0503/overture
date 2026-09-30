package common

import "testing"

func TestDomainECSLookupLongestSuffix(t *testing.T) {
	m := DomainECSMap{
		{Domain: "example.com", Policy: "auto", ExternalIP: "203.0.113.1"},
		{Domain: "cdn.example.com", Policy: "manual", ExternalIP: "198.51.100.10"},
		{Domain: "ads.example.net", Policy: "disable"},
	}
	got := m.Lookup("www.cdn.example.com.")
	if got == nil || got.Policy != "manual" || got.ExternalIP != "198.51.100.10" {
		t.Fatalf("cdn child: %+v", got)
	}
	got = m.Lookup("www.example.com")
	if got == nil || got.Policy != "auto" || got.ExternalIP != "203.0.113.1" {
		t.Fatalf("example.com child: %+v", got)
	}
	got = m.Lookup("ads.example.net.")
	if got == nil || got.Policy != "disable" {
		t.Fatalf("exact disable: %+v", got)
	}
	if m.Lookup("unrelated.org.") != nil {
		t.Fatal("unrelated domain should not match")
	}
	if (DomainECSMap{}).Lookup("example.com") != nil {
		t.Fatal("empty map should miss")
	}
}
