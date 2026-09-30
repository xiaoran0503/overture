// Package metrics exposes Prometheus instrumentation for the inbound server
// and the response cache. Exported only through the debug HTTP listener
// (/metrics); the listener is token-protected outside loopback.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// DNSQueriesTotal counts inbound DNS queries by transport (udp/tcp).
	DNSQueriesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "overture",
		Name:      "dns_queries_total",
		Help:      "Total inbound DNS queries processed, by transport.",
	}, []string{"transport"})

	// DNSResponsesTotal counts responses written to clients, by rcode.
	DNSResponsesTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "overture",
		Name:      "dns_responses_total",
		Help:      "Total DNS responses written to clients, by rcode.",
	}, []string{"rcode"})

	// DoHRequestsTotal counts DoH requests received on the debug HTTP listener.
	DoHRequestsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "overture",
		Name:      "doh_requests_total",
		Help:      "Total DNS-over-HTTPS requests received.",
	})

	// CacheHitsTotal counts response-cache hits (local or Redis).
	CacheHitsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "overture",
		Name:      "cache_hits_total",
		Help:      "Total response cache hits.",
	})

	// CacheMissesTotal counts response-cache misses.
	CacheMissesTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "overture",
		Name:      "cache_misses_total",
		Help:      "Total response cache misses.",
	})

	// Up reports process liveness (always 1 while the process is serving).
	Up = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "overture",
		Name:      "up",
		Help:      "1 while the process is serving.",
	})

	// UpstreamUp is 1 while a configured upstream is considered healthy.
	UpstreamUp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "overture",
		Name:      "upstream_up",
		Help:      "1 while the named upstream is healthy, 0 when marked down.",
	}, []string{"name", "address", "group"})
)

func init() {
	prometheus.MustRegister(DNSQueriesTotal, DNSResponsesTotal, DoHRequestsTotal,
		CacheHitsTotal, CacheMissesTotal, Up, UpstreamUp)
	Up.Set(1)
}

// Handler serves the Prometheus exposition format.
func Handler() http.Handler { return promhttp.Handler() }
