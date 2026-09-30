package health

import (
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/metrics"
	"github.com/shawn1m/overture/core/outbound/clients/resolver"
	log "github.com/sirupsen/logrus"
)

// Options controls active probing and the consecutive-failure window.
// Zero numeric fields are replaced with defaults when Enable is true.
type Options struct {
	Enable           bool
	Interval         int
	Timeout          int
	FailThreshold    int
	RecoverThreshold int
	Domain           string
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = 30
	}
	if o.Timeout <= 0 {
		o.Timeout = 5
	}
	if o.FailThreshold <= 0 {
		o.FailThreshold = 3
	}
	if o.RecoverThreshold <= 0 {
		o.RecoverThreshold = 2
	}
	if strings.TrimSpace(o.Domain) == "" {
		o.Domain = "example.com."
	}
	if !strings.HasSuffix(o.Domain, ".") {
		o.Domain += "."
	}
	return o
}

// Target is one upstream the checker probes and whose liveness it tracks.
type Target struct {
	Group    string
	Upstream *common.DNSUpstream
	Resolver resolver.Resolver
}

type state struct {
	up        bool
	fails     int
	successes int
	group     string
	name      string
	address   string
}

// Checker tracks upstream liveness from real exchanges (passive) and an
// optional background probe (active). Nil-safe: a nil Checker is always
// healthy and Record is a no-op, matching the historical always-query
// behaviour.
type Checker struct {
	opts    Options
	targets []Target

	mu     sync.RWMutex
	states map[string]*state

	stop chan struct{}
	wg   sync.WaitGroup
}

// New builds a checker. The caller must Start it. If opts.Enable is false
// the returned checker is nil.
func New(opts Options, targets []Target) *Checker {
	if !opts.Enable {
		return nil
	}
	opts = opts.withDefaults()
	c := &Checker{
		opts:    opts,
		targets: targets,
		states:  make(map[string]*state),
		stop:    make(chan struct{}),
	}
	for _, t := range targets {
		if t.Upstream == nil {
			continue
		}
		k := key(t.Upstream)
		if _, ok := c.states[k]; ok {
			continue
		}
		st := &state{
			up:      true,
			group:   t.Group,
			name:    t.Upstream.Name,
			address: t.Upstream.Address,
		}
		c.states[k] = st
		metrics.UpstreamUp.WithLabelValues(st.name, st.address, st.group).Set(1)
	}
	return c
}

// Start launches the active probe loop. Safe to call on nil.
func (c *Checker) Start() {
	if c == nil {
		return
	}
	log.Infof("Upstream health check enabled (interval=%ds timeout=%ds failThreshold=%d recoverThreshold=%d domain=%s)",
		c.opts.Interval, c.opts.Timeout, c.opts.FailThreshold, c.opts.RecoverThreshold, c.opts.Domain)
	c.wg.Add(1)
	go c.loop()
}

// Stop ends the probe loop and waits for it. Safe to call on nil.
func (c *Checker) Stop() {
	if c == nil {
		return
	}
	select {
	case <-c.stop:
		return
	default:
		close(c.stop)
	}
	c.wg.Wait()
}

// Healthy reports whether queries should be sent to u. Unknown or disabled
// upstreams are treated as healthy (fail-open).
func (c *Checker) Healthy(u *common.DNSUpstream) bool {
	if c == nil || u == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	st, ok := c.states[key(u)]
	if !ok {
		return true
	}
	return st.up
}

// Record notes a real exchange or probe result. Consecutive failures past
// FailThreshold mark the upstream down; consecutive successes past
// RecoverThreshold bring it back.
func (c *Checker) Record(u *common.DNSUpstream, ok bool) {
	if c == nil || u == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, exists := c.states[key(u)]
	if !exists {
		st = &state{up: true, name: u.Name, address: u.Address}
		c.states[key(u)] = st
	}
	if ok {
		st.fails = 0
		st.successes++
		if !st.up && st.successes >= c.opts.RecoverThreshold {
			st.up = true
			log.Infof("Upstream %s (%s) recovered after %d successes", st.name, st.address, st.successes)
			metrics.UpstreamUp.WithLabelValues(st.name, st.address, st.group).Set(1)
		}
		return
	}
	st.successes = 0
	st.fails++
	if st.up && st.fails >= c.opts.FailThreshold {
		st.up = false
		log.Warnf("Upstream %s (%s) marked down after %d failures", st.name, st.address, st.fails)
		metrics.UpstreamUp.WithLabelValues(st.name, st.address, st.group).Set(0)
	}
}

func (c *Checker) loop() {
	defer c.wg.Done()
	c.probeAll()
	ticker := time.NewTicker(time.Duration(c.opts.Interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.probeAll()
		}
	}
}

func (c *Checker) probeAll() {
	for _, t := range c.targets {
		select {
		case <-c.stop:
			return
		default:
		}
		c.probe(t)
	}
}

func (c *Checker) probe(t Target) {
	if t.Resolver == nil || t.Upstream == nil {
		return
	}
	q := new(dns.Msg)
	q.SetQuestion(c.opts.Domain, dns.TypeA)
	q.RecursionDesired = true

	type outcome struct {
		resp *dns.Msg
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, err := t.Resolver.Exchange(q)
		done <- outcome{resp, err}
	}()

	timeout := time.Duration(c.opts.Timeout) * time.Second
	select {
	case <-c.stop:
		return
	case <-time.After(timeout):
		log.Debugf("Health probe %s (%s) timed out", t.Upstream.Name, t.Upstream.Address)
		c.Record(t.Upstream, false)
	case o := <-done:
		ok := o.err == nil && o.resp != nil
		if !ok {
			log.Debugf("Health probe %s (%s) failed: %v", t.Upstream.Name, t.Upstream.Address, o.err)
		}
		c.Record(t.Upstream, ok)
	}
}

func key(u *common.DNSUpstream) string {
	return u.Protocol + "|" + u.Address
}
