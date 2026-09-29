package config

import "testing"

// FuzzApplyJSON guards the JSON hot-reload path (/reload/config): arbitrary
// payloads must be rejected with an error, never panic or wedge the process.
func FuzzApplyJSON(f *testing.F) {
	base := testConfig()
	f.Add([]byte(`{"bindAddress": "127.0.0.1:53"}`))
	f.Add([]byte(`{"primaryDNS": [{"name":"a","address":"1.1.1.1","protocol":"udp"}]}`))
	f.Add([]byte(`{"debugHTTPToken": "x"}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ApplyJSON(base, data) // must never panic
	})
}
