package full

import "testing"

func TestMapIsCaseInsensitive(t *testing.T) {
	m := &Map{DataMap: make(map[string][]string)}
	if err := m.Insert("LocalHost", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"localhost", "LOCALHOST", "LocalHost"} {
		got := m.Get(q)
		if len(got) != 1 || got[0] != "127.0.0.1" {
			t.Errorf("Map.Get(%q) = %v, want [127.0.0.1]", q, got)
		}
	}
}
