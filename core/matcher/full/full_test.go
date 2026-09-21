package full

import "testing"

func TestMapIsCaseInsensitive(t *testing.T) {
	m := &Map{DataMap: make(map[string]struct{})}
	if err := m.Insert("Example.COM"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"example.com", "EXAMPLE.COM", "Example.Com"} {
		if !m.Has(q) {
			t.Errorf("Map.Has(%q) = false, want true", q)
		}
	}
	if m.Has("example.org") {
		t.Error("Map.Has(example.org) = true, want false")
	}
}

func TestListIsCaseInsensitive(t *testing.T) {
	l := &List{}
	if err := l.Insert("Example.COM"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"example.com", "EXAMPLE.COM", "Example.Com"} {
		if !l.Has(q) {
			t.Errorf("List.Has(%q) = false, want true", q)
		}
	}
	if l.Has("example.org") {
		t.Error("List.Has(example.org) = true, want false")
	}
}
