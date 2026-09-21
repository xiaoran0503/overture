package config

import (
	"os"
	"path/filepath"
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

func testConfig() *Config {
	config := &Config{BindAddress: "127.0.0.1:5353"}
	config.PrimaryDNS = []*common.DNSUpstream{{Name: "primary", Address: "127.0.0.1:53", Protocol: "udp", Timeout: 3}}
	return config
}
