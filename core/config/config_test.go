package config

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shawn1m/overture/core/common"
)

func TestApplyJSONRebuildsRuntimeState(t *testing.T) {
	current := testConfig()
	current.CacheSize = 10
	current, err := Build(current)
	if err != nil {
		t.Fatal(err)
	}
	oldCache := current.Cache
	next, err := ApplyJSON(current, []byte(`{"cacheSize":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if next.CacheSize != 0 || next.Cache != nil {
		t.Fatalf("cache was not rebuilt: size=%d cache=%v", next.CacheSize, next.Cache)
	}
	if oldCache == next.Cache {
		t.Fatal("JSON reload retained the old cache")
	}
}

func TestLoadYAMLv3HistoricalScalars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	content := "bindAddress: 127.0.0.1:5353\n" +
		"dohEnabled: yes\n" +
		"primaryDNS:\n  - &upstream\n    name: primary\n    address: 127.0.0.1:53\n    protocol: udp\n    timeout: 3\n" +
		"alternativeDNS:\n  - <<: *upstream\n    name: alternative\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.DohEnabled || len(loaded.AlternativeDNS) != 1 || loaded.AlternativeDNS[0].Address != "127.0.0.1:53" {
		t.Fatalf("historical YAML was not preserved: %#v", loaded)
	}
}

func TestDomainTTLFileSkipsInvalidValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ttl.txt")
	if err := os.WriteFile(path, []byte("valid.example 30\ninvalid.example nope\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values := getDomainTTLMap(path)
	if values["valid.example"] != 30 {
		t.Fatalf("valid TTL = %d, want 30", values["valid.example"])
	}
	if _, ok := values["invalid.example"]; ok {
		t.Fatal("invalid TTL was inserted")
	}
}

func TestDomainMatcherNormalizesTrailingDot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")
	if err := os.WriteFile(path, []byte("example.com.\nalt.example.org\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m := initDomainMatcher(path, "", "full-map")
	if m == nil {
		t.Fatal("matcher is nil")
	}
	if !m.Has("example.com") {
		t.Error("entry 'example.com.' did not match query 'example.com'")
	}
	if !m.Has("alt.example.org") {
		t.Error("entry 'alt.example.org' did not match query 'alt.example.org'")
	}

	// suffix-tree must also survive trailing dots without empty segments.
	s := initDomainMatcher(path, "suffix-tree", "")
	if s == nil {
		t.Fatal("suffix matcher is nil")
	}
	if !s.Has("www.example.com") {
		t.Error("suffix-tree entry 'example.com.' did not match 'www.example.com'")
	}
}

func TestIPNetworkFileAcceptsCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "networks.txt")
	if err := os.WriteFile(path, []byte("10.0.0.0/8\r\n192.168.0.0/16\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	set := getIPNetworkSet(path)
	if set == nil {
		t.Fatal("IP network set is nil")
	}
	if !set.Contains(net.ParseIP("10.1.2.3"), false, "") {
		t.Error("CRLF entry 10.0.0.0/8 did not load")
	}
	if !set.Contains(net.ParseIP("192.168.9.9"), false, "") {
		t.Error("CRLF entry 192.168.0.0/16 did not load")
	}
}

func TestBuildRequiresPrimaryDNS(t *testing.T) {
	config := testConfig()
	config.PrimaryDNS = nil
	if _, err := Build(config); err == nil {
		t.Fatal("Build accepted a config without primaryDNS")
	}
}

func TestBuildRejectsNegativeMinimumTTL(t *testing.T) {
	config := testConfig()
	config.MinimumTTL = -1
	if _, err := Build(config); err == nil {
		t.Fatal("Build accepted a negative minimumTTL")
	}
}

func TestApplyJSONOverlayDoesNotMutateCurrent(t *testing.T) {
	current, err := Build(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	before := current.PrimaryDNS[0]

	next, err := ApplyJSON(current, []byte(`{"primaryDNS":[{"name":"changed","address":"9.9.9.9:53","protocol":"udp","timeout":5}],"rejectQType":[255]}`))
	if err != nil {
		t.Fatal(err)
	}

	// The running configuration must be untouched.
	if current.PrimaryDNS[0].Name != before.Name || current.PrimaryDNS[0].Address != before.Address {
		t.Fatal("ApplyJSON mutated the running configuration")
	}
	if current.RejectQType != nil {
		t.Fatal("ApplyJSON mutated the running rejectQType")
	}

	// The overlay must not share backing arrays or element objects.
	if next.PrimaryDNS[0] == current.PrimaryDNS[0] {
		t.Fatal("overlay shares DNSUpstream objects with the running config")
	}
	if len(next.RejectQType) > 0 && len(current.RejectQType) > 0 && &next.RejectQType[0] == &current.RejectQType[0] {
		t.Fatal("overlay shares the rejectQType backing array")
	}

	// Forward assertions: the overlay must actually be applied, so a future
	// regression that silently drops the overlay is caught too.
	if next.PrimaryDNS[0].Name != "changed" || next.PrimaryDNS[0].Address != "9.9.9.9:53" {
		t.Fatal("ApplyJSON did not apply the primaryDNS overlay")
	}
	if len(next.RejectQType) != 1 || next.RejectQType[0] != 255 {
		t.Fatalf("ApplyJSON did not apply the rejectQType overlay: %v", next.RejectQType)
	}
}

func TestApplyJSONConcurrentReadsUnderRace(t *testing.T) {
	current, err := Build(testConfig())
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if len(current.PrimaryDNS) > 0 {
						_ = current.PrimaryDNS[0].Address
					}
					_ = current.RejectQType
				}
			}
		}()
	}

	for i := 0; i < 50; i++ {
		if _, err := ApplyJSON(current, []byte(`{"primaryDNS":[{"name":"n","address":"127.0.0.1:53","protocol":"udp","timeout":3}]}`)); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func testConfig() *Config {
	config := &Config{BindAddress: "127.0.0.1:5353"}
	config.PrimaryDNS = []*common.DNSUpstream{{Name: "primary", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}}
	return config
}
