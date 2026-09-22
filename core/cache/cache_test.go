package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
)

func TestEvictRandomKeepsCapacity(t *testing.T) {
	c := New(2, "", 0)

	for i := 0; i < 3; i++ {
		msg := new(dns.Msg)
		msg.SetQuestion("example.com.", dns.TypeA)
		c.InsertMessageToLocal(string(rune('a'+i)), msg, 60)
	}

	if got := len(c.table); got != c.capacity {
		t.Fatalf("cache length = %d, want %d", got, c.capacity)
	}
}

func TestInsertMessageSkipsTruncated(t *testing.T) {
	c := New(4, "", 0)
	message := new(dns.Msg)
	message.SetQuestion("trunc.example.", dns.TypeA)
	message.Truncated = true
	a, _ := dns.NewRR("trunc.example. 60 IN A 5.6.7.8")
	message.Answer = []dns.RR{a}
	c.InsertMessage("trunc.example. 1", message, 60)

	// A truncated (TC=1) response carries only partial answers; it must not
	// be cached, otherwise a later client would get a TC=0 replay of an
	// incomplete answer and never retry over TCP.
	if hit := c.Hit("trunc.example. 1", dns.Question{Name: "trunc.example.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("truncated response was cached")
	}
}

func TestHitPreservesPerRecordTTL(t *testing.T) {
	c := New(2, "", 0)
	message := new(dns.Msg)
	message.SetQuestion("example.com.", dns.TypeA)
	first, _ := dns.NewRR("example.com. 5 IN A 192.0.2.1")
	second, _ := dns.NewRR("example.com. 3 IN A 192.0.2.2")
	message.Answer = []dns.RR{first, second}
	c.InsertMessageToLocal("key", message, 60)

	c.Lock()
	c.table["key"].storedAt = time.Now().Add(-2 * time.Second)
	c.table["key"].expiration = time.Now().Add(time.Second)
	c.Unlock()

	hit := c.Hit("key", dns.Question{Name: "EXAMPLE.COM.", Qtype: dns.TypeA}, 123)
	if hit == nil {
		t.Fatal("expected cache hit")
	}
	if got := hit.Question[0].Name; got != "EXAMPLE.COM." {
		t.Fatalf("question name = %q, want the exact query text %q", got, "EXAMPLE.COM.")
	}
	if got := hit.Answer[0].Header().Ttl; got != 3 {
		t.Fatalf("first TTL = %d, want 3", got)
	}
	if got := hit.Answer[1].Header().Ttl; got != 1 {
		t.Fatalf("second TTL = %d, want 1", got)
	}
}

func TestHitToleratesNilMessage(t *testing.T) {
	c := New(2, "", 0)
	message := new(dns.Msg)
	message.SetQuestion("example.com.", dns.TypeA)
	c.InsertMessageToLocal("key", message, 60)
	c.Lock()
	c.table["key"].msg = nil
	c.Unlock()

	if hit := c.Hit("key", dns.Question{Name: "example.com.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("Hit returned a nil-message entry instead of dropping it")
	}
}

func TestCacheTTLRespectsMinimumAcrossSections(t *testing.T) {
	message := new(dns.Msg)
	message.SetQuestion("example.com.", dns.TypeA)
	a, _ := dns.NewRR("example.com. 3600 IN A 192.0.2.1")
	soa, _ := dns.NewRR("example.com. 30 IN SOA ns.example.com. hostmaster.example.com. 1 7200 900 1209600 60")
	message.Answer = []dns.RR{a}
	message.Ns = []dns.RR{soa}

	common.SetMinimumTTL(message, 3600)

	if got := cacheTTL(message, 60); got < 3600*time.Second {
		t.Fatalf("cacheTTL = %v, want >= 3600s after SetMinimumTTL", got)
	}
}

func TestKeyNormalizesCase(t *testing.T) {
	upper := dns.Question{Name: "WWW.EXAMPLE.COM.", Qtype: dns.TypeA}
	lower := dns.Question{Name: "www.example.com.", Qtype: dns.TypeA}
	if Key(upper, "1.2.3.4") != Key(lower, "1.2.3.4") {
		t.Fatalf("cache keys differ by case: %q vs %q", Key(upper, "1.2.3.4"), Key(lower, "1.2.3.4"))
	}
}

func TestDumpIsSafeDuringWrites(t *testing.T) {
	c := New(16, "", 0)
	message := new(dns.Msg)
	message.SetQuestion("example.com.", dns.TypeA)
	message.Answer = append(message.Answer, &dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}})

	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				c.InsertMessageToLocal(string(rune('a'+j%16)), message, 60)
				c.Dump(j%2 == 0)
			}
		}()
	}
	group.Wait()
}
