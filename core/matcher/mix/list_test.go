package mix

import "testing"

func TestListIsCaseInsensitive(t *testing.T) {
	l := &List{}
	for _, rule := range []string{"example.com", "keyword:example", "full:exact.example.org"} {
		if err := l.Insert(rule); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		query string
		want  bool
	}{
		{"www.example.com", true},      // domain suffix
		{"WWW.EXAMPLE.COM", true},      // domain suffix, upper case
		{"deep.sub.example.com", true}, // deeper suffix
		{"sub.example.org", true},      // keyword
		{"SUB.EXAMPLE.ORG", true},      // keyword, upper case
		{"EXACT.EXAMPLE.ORG", true},    // full, upper case
		{"exact.example.org", true},    // full, lower case
		{"other.net", false},           // nothing matches
	}
	for _, tc := range cases {
		if got := l.Has(tc.query); got != tc.want {
			t.Errorf("List.Has(%q) = %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestListRegexKeepsCaseSemantics(t *testing.T) {
	l := &List{}
	if err := l.Insert("regex:^www\\."); err != nil {
		t.Fatal(err)
	}
	// Patterns are stored lower-cased (existing Insert behavior) and matched
	// against the original query, so regex rules stay case-sensitive.
	if !l.Has("www.example.net") {
		t.Error("lower-case query did not match")
	}
	if l.Has("WWW.example.net") {
		t.Error("upper-case query matched a lower-case pattern")
	}
}

func TestInsertRejectsInvalidRegex(t *testing.T) {
	l := &List{}
	if err := l.Insert("regex:foo("); err == nil {
		t.Fatal("Insert accepted an invalid regex rule")
	}
	// The broken rule must not reach the table, so the query path never panics.
	if l.Has("www.example.com") {
		t.Error("invalid regex rule should not match anything")
	}
}

func TestInsertAcceptsRegexWithColons(t *testing.T) {
	l := &List{}
	if err := l.Insert("regex:^https?://"); err != nil {
		t.Fatalf("regex rule containing colons was rejected: %s", err)
	}
	if !l.Has("https://example.com") {
		t.Error("regex rule with colons did not match")
	}
}

func TestListDomainMissDoesNotHideLaterRules(t *testing.T) {
	l := &List{}
	for _, rule := range []string{"example.com", "keyword:example"} {
		if err := l.Insert(rule); err != nil {
			t.Fatal(err)
		}
	}
	// "notexample.com" ends with "example.com" but not as a subdomain; the
	// keyword rule that follows must still be evaluated.
	if !l.Has("notexample.com") {
		t.Error("Has(notexample.com) = false, want true via later keyword rule")
	}

	single := &List{}
	if err := single.Insert("example.com"); err != nil {
		t.Fatal(err)
	}
	if !single.Has("a.example.com") {
		t.Error("subdomain should match")
	}
	if single.Has("notexample.com") {
		t.Error("mid-label suffix must not match a single domain rule")
	}
}
