package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/metrics"
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

func mustCacheRR(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		panic(err)
	}
	return rr
}

func TestElemBinaryRoundTrip(t *testing.T) {
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	msg.Answer = []dns.RR{mustCacheRR("example.com. 60 IN A 192.0.2.1")}
	want := &elem{expiration: time.Now().Add(60 * time.Second), storedAt: time.Now(), msg: msg}

	b, err := want.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var got elem
	if err := got.UnmarshalBinary(b); err != nil {
		t.Fatal(err)
	}
	if !got.expiration.Equal(want.expiration) || !got.storedAt.Equal(want.storedAt) {
		t.Fatalf("binary round trip lost metadata: exp %v/%v stored %v/%v", got.expiration, want.expiration, got.storedAt, want.storedAt)
	}
	if got.msg == nil || len(got.msg.Answer) != 1 {
		t.Fatalf("binary round trip lost message: %v", got.msg)
	}
	if got.msg.Answer[0].(*dns.A).A.String() != "192.0.2.1" {
		t.Fatalf("binary round trip corrupted rdata: %v", got.msg.Answer[0])
	}
}

func TestNewZeroCapacityIsNil(t *testing.T) {
	if got := New(0, "", 0); got != nil {
		t.Fatal("zero capacity should return nil")
	}
}

func TestNewInvalidRedisURLFallsBackToLocal(t *testing.T) {
	c := New(10, "://bad-url", 0)
	if c == nil {
		t.Fatal("invalid Redis URL must not disable caching")
	}
	if c.redisClient != nil {
		t.Fatal("invalid Redis URL should fall back to a local cache")
	}
	if got := c.Capacity(); got != 10 {
		t.Fatalf("Capacity = %d, want 10", got)
	}
}

func TestCapacityCloseAndNilSafety(t *testing.T) {
	c := New(5, "", 0)
	if got := c.Capacity(); got != 5 {
		t.Fatalf("Capacity = %d, want 5", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("local Close returned %v", err)
	}
	var nc *Cache
	if got := nc.Capacity(); got != 0 {
		t.Fatalf("nil cache Capacity = %d, want 0", got)
	}
	if err := nc.Close(); err != nil {
		t.Fatalf("nil cache Close returned %v", err)
	}
	nc.Remove("key") // must not panic
}

func TestRemoveDeletesLocalEntry(t *testing.T) {
	c := New(4, "", 0)
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	c.InsertMessageToLocal("key", msg, 60)
	c.Remove("key")
	if hit := c.Hit("key", dns.Question{Name: "example.com.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("entry survived Remove")
	}
}

func TestInsertMessageGuardsNilAndZeroCapacity(t *testing.T) {
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)

	var nc *Cache
	nc.InsertMessage("k", msg, 60) // must not panic

	c := New(4, "", 0)
	c.InsertMessage("k", nil, 60) // nil message must be ignored
	if hit := c.Hit("k", dns.Question{Name: "example.com.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("nil message was cached")
	}
}

func TestHitExpiredEntryIsRemoved(t *testing.T) {
	c := New(4, "", 0)
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	c.InsertMessageToLocal("key", msg, 60)
	c.Lock()
	c.table["key"].expiration = time.Now().Add(-time.Second)
	c.Unlock()

	if hit := c.Hit("key", dns.Question{Name: "example.com.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("expired entry should miss")
	}
	if _, ok := c.table["key"]; ok {
		t.Fatal("expired entry should be removed from the table")
	}
}

func TestInsertMessageRedisUnreachableDegrades(t *testing.T) {
	c := &Cache{capacity: 4, table: make(map[string]*elem),
		redisClient: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 300 * time.Millisecond})}
	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	msg.Answer = []dns.RR{mustCacheRR("example.com. 60 IN A 192.0.2.1")}
	c.InsertMessage("key", msg, 60) // must not panic, warns
	if hit := c.Hit("key", dns.Question{Name: "example.com.", Qtype: dns.TypeA}, 1); hit != nil {
		t.Fatal("unreachable Redis must not yield a cached hit")
	}
}

func TestCacheHitMissMetrics(t *testing.T) {
	c := New(4, "", 0)
	q := dns.Question{Name: "example.com.", Qtype: dns.TypeA}
	beforeMiss := testutil.ToFloat64(metrics.CacheMissesTotal)
	beforeHit := testutil.ToFloat64(metrics.CacheHitsTotal)

	c.Hit("k", q, 1) // miss
	if got := testutil.ToFloat64(metrics.CacheMissesTotal); got != beforeMiss+1 {
		t.Fatalf("misses = %v, want %v", got, beforeMiss+1)
	}

	msg := new(dns.Msg)
	msg.SetQuestion("example.com.", dns.TypeA)
	c.InsertMessageToLocal("k", msg, 60)
	c.Hit("k", q, 1) // hit
	if got := testutil.ToFloat64(metrics.CacheHitsTotal); got != beforeHit+1 {
		t.Fatalf("hits = %v, want %v", got, beforeHit+1)
	}
}
