// Package inbound implements dns server for inbound connection.
package inbound

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coredns/coredns/plugin/pkg/dnsutil"
	"github.com/coredns/coredns/plugin/pkg/doh"
	"github.com/coredns/coredns/plugin/pkg/response"
	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
	"github.com/shawn1m/overture/core/common"
	"github.com/shawn1m/overture/core/metrics"
	log "github.com/sirupsen/logrus"

	"github.com/shawn1m/overture/core/outbound"
)

type Server struct {
	bindAddress      string
	debugHttpAddress string
	httpToken        string
	dispatcher       outbound.Dispatcher
	rejectQType      []uint16
	HTTPMux          *http.ServeMux
	ctx              context.Context
	cancel           context.CancelFunc
	dohEnabled       bool
	doh3             DoH3Config
	started          chan struct{}
	done             chan struct{}
}

// DoH3Config is the optional dedicated DNS-over-HTTP/3 listener. Empty Enable
// keeps the historical plaintext DoH-on-debug-HTTP setup.
type DoH3Config struct {
	Enable   bool
	Address  string
	CertFile string
	KeyFile  string
}

func NewServer(bindAddress string, debugHTTPAddress string, dispatcher outbound.Dispatcher, rejectQType []uint16, dohEnabled bool, httpToken string) *Server {
	s := &Server{
		bindAddress:      bindAddress,
		debugHttpAddress: debugHTTPAddress,
		httpToken:        httpToken,
		dispatcher:       dispatcher,
		rejectQType:      rejectQType,
		dohEnabled:       dohEnabled,
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.HTTPMux = http.NewServeMux()
	s.started = make(chan struct{})
	s.done = make(chan struct{})
	return s
}

// SetDoH3 attaches the optional HTTP/3 DNS listener. Must be called before Run.
func (s *Server) SetDoH3(cfg DoH3Config) {
	s.doh3 = cfg
}

func (s *Server) ServeDNSHttp(w http.ResponseWriter, r *http.Request) {
	metrics.DoHRequestsTotal.Inc()
	if r.URL.Path != doh.Path {
		http.Error(w, "", http.StatusNotFound)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost && !isDNSMessageContentType(r.Header.Get("Content-Type")) {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}

	q, err := doh.RequestToMsg(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(q.Question) == 0 {
		http.Error(w, "missing question section", http.StatusBadRequest)
		return
	}
	if q.Opcode != dns.OpcodeQuery {
		// NOTIFY and other non-query opcodes are not supported.
		http.Error(w, "not implemented", http.StatusNotImplemented)
		return
	}

	// Create a DoHWriter with the correct addresses in it.
	inboundIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	// X-Forwarded-For is trusted only when the direct peer is a
	// reserved/loopback address. Do not expose DoH directly to the public
	// internet without a trusted reverse proxy: a public client could
	// otherwise forge this header and inject an arbitrary ECS address.
	// The header may carry a chain ("client, proxy1"); only the leftmost
	// value is the original client.
	forwardIP := r.Header.Get("X-Forwarded-For")
	if idx := strings.IndexByte(forwardIP, ','); idx != -1 {
		forwardIP = strings.TrimSpace(forwardIP[:idx])
	}
	if net.ParseIP(forwardIP) != nil && common.ReservedIPNetworkList.Contains(net.ParseIP(inboundIP), false, "") {
		inboundIP = forwardIP
	}
	log.Debugf("Question from %s: %s", inboundIP, q.Question[0].String())

	for _, qt := range s.rejectQType {
		if isQuestionType(q, qt) {
			log.Debugf("Reject %s: %s", inboundIP, q.Question[0].String())
			http.Error(w, "Rejected", http.StatusForbidden)
			return
		}
	}

	responseMessage := s.dispatcher.Exchange(q, inboundIP)

	if responseMessage == nil {
		http.Error(w, "No response", http.StatusInternalServerError)
		return
	}

	responseMessage.Compress = true

	buf, err := responseMessage.Pack()
	if err != nil {
		log.Errorf("Failed to pack DoH response: %s", err)
		http.Error(w, "pack failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", doh.MimeType)
	w.Header().Set("Cache-Control", dohCacheControl(responseMessage))
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(http.StatusOK)

	w.Write(buf)
}

func (s *Server) ServeDNSHttp3(w http.ResponseWriter, r *http.Request) {
	metrics.DoH3RequestsTotal.Inc()
	s.ServeDNSHttp(w, r)
}

func (s *Server) DumpCache(w http.ResponseWriter, req *http.Request) {
	if s.dispatcher.Cache == nil {
		io.WriteString(w, "error: cache not enabled")
		return
	}

	type answer struct {
		Name  string `json:"name"`
		TTL   int    `json:"ttl"`
		Type  string `json:"type"`
		Rdata string `json:"rdata"`
	}

	type response struct {
		Length   int                  `json:"length"`
		Capacity int                  `json:"capacity"`
		Body     map[string][]*answer `json:"body"`
	}

	query := req.URL.Query()
	nobody := true
	if t := query.Get("nobody"); strings.ToLower(t) == "false" {
		nobody = false
	}

	rs, l := s.dispatcher.Cache.Dump(nobody)
	body := make(map[string][]*answer)

	for k, es := range rs {
		var answers []*answer
		for _, e := range es {
			ts := strings.Split(e, "\t")
			ttl, _ := strconv.Atoi(ts[1])
			r := &answer{
				Name:  ts[0],
				TTL:   ttl,
				Type:  ts[3],
				Rdata: strings.Join(ts[4:], "\t"),
			}
			answers = append(answers, r)
		}
		body[strings.TrimSpace(k)] = answers
	}

	res := response{
		Body:     body,
		Length:   l,
		Capacity: s.dispatcher.Cache.Capacity(),
	}

	responseBytes, err := json.Marshal(&res)
	if err != nil {
		io.WriteString(w, err.Error())
		return
	}

	_, _ = w.Write(responseBytes)
}

func (s *Server) Run() error {
	defer close(s.done)
	close(s.started)

	if s.debugHttpAddress != "" && !isLoopbackAddress(s.debugHttpAddress) && s.httpToken == "" {
		return fmt.Errorf("debug HTTP address %s is not loopback; set debugHTTPToken before exposing it", s.debugHttpAddress)
	}

	mux := dns.NewServeMux()
	mux.Handle(".", s)

	wg := new(sync.WaitGroup)
	wg.Add(2)

	// A listener failure must not kill the process: the pre-reload CheckBind
	// narrows the window but cannot fully close the TOCTOU race, and a bind
	// error here should be reported to the caller instead of exiting.
	var listenErr error
	var errMu sync.Mutex
	reportListenErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if listenErr == nil {
			listenErr = err
		}
		errMu.Unlock()
		// Tear down the sibling listeners (e.g. a debug HTTP server that did
		// bind successfully) so Run can return and the caller can roll back.
		s.cancel()
	}

	log.Infof("Overture is listening on %s", s.bindAddress)

	for _, p := range [2]string{"tcp", "udp"} {
		go func(p string) {
			defer wg.Done()

			// Manual create server inorder to have a way to close it.
			// UDPSize must be raised from the miekg default (MinMsgSize=512):
			// a client query with EDNS0 padding or several options can exceed
			// 512 bytes and would otherwise be read truncated and answered
			// FORMERR. dns.MaxMsgSize (65535) is the DNS-over-UDP protocol
			// ceiling and avoids introducing a new hidden truncation bound.
			srv := &dns.Server{Addr: s.bindAddress, Net: p, Handler: mux, UDPSize: dns.MaxMsgSize}
			go func() {
				<-s.ctx.Done()
				log.Warnf("Shutting down the server on protocol %s", p)
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.ShutdownContext(shutdownCtx)
			}()
			// miekg/dns returns nil after a graceful ShutdownContext, so a
			// non-nil error here is a real listener failure, not a teardown.
			if err := srv.ListenAndServe(); err != nil {
				log.Errorf("Listening on port %s failed: %s", p, err)
				reportListenErr(fmt.Errorf("listen %s on %s: %w", p, s.bindAddress, err))
			}
		}(p)
	}

	if s.debugHttpAddress != "" {
		s.registerDebugHandlers()

		wg.Add(1)
		go func() {
			defer wg.Done()

			// Manual create server inorder to have a way to close it.
			srv := &http.Server{
				Addr:              s.debugHttpAddress,
				Handler:           s.httpHandler(),
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       15 * time.Second,
				WriteTimeout:      15 * time.Second,
				IdleTimeout:       60 * time.Second,
			}
			go func() {
				<-s.ctx.Done()
				log.Warnf("Shutting down debug HTTP server")
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				srv.Shutdown(shutdownCtx)
			}()

			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Errorf("Debug HTTP Server Listen on port %s failed: %s", s.debugHttpAddress, err)
				reportListenErr(fmt.Errorf("listen debug HTTP on %s: %w", s.debugHttpAddress, err))
			}
		}()
	}

	if s.doh3.Enable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cert, err := tls.LoadX509KeyPair(s.doh3.CertFile, s.doh3.KeyFile)
			if err != nil {
				reportListenErr(fmt.Errorf("load doh3 certificate: %w", err))
				return
			}
			tlsConf := http3.ConfigureTLSConfig(&tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			})
			h3mux := http.NewServeMux()
			h3mux.HandleFunc(doh.Path, s.ServeDNSHttp3)
			h3 := &http3.Server{Addr: s.doh3.Address, Handler: h3mux, TLSConfig: tlsConf}
			go func() {
				<-s.ctx.Done()
				log.Warnf("Shutting down DoH3 server")
				_ = h3.Close()
			}()
			log.Infof("DNS-over-HTTP/3 server listening on %s", s.doh3.Address)
			if err := h3.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Errorf("DoH3 listen on %s failed: %s", s.doh3.Address, err)
				reportListenErr(fmt.Errorf("listen doh3 on %s: %w", s.doh3.Address, err))
			}
		}()
	}

	wg.Wait()
	return listenErr
}

// registerDebugHandlers wires the debug HTTP endpoints. Kept as a method so
// tests can exercise them without binding a real listener.
func (s *Server) registerDebugHandlers() {
	s.HTTPMux.HandleFunc("/cache", s.DumpCache)
	// /healthz is a liveness probe for deployments: a listener failure now
	// returns an error instead of exiting, so external health checks are the
	// authoritative way to detect a dead (but alive) process. The endpoint is
	// token-protected like every debug HTTP path; on loopback with no token
	// configured it is open to localhost.
	s.HTTPMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	s.HTTPMux.Handle("/metrics", metrics.Handler())
	s.HTTPMux.HandleFunc("/debug/pprof/", pprof.Index)
	s.HTTPMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	s.HTTPMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	s.HTTPMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	s.HTTPMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	if s.dohEnabled {
		log.Info("Dns over http server started!")
		s.HTTPMux.HandleFunc(doh.Path, s.ServeDNSHttp)
	}
}

func (s *Server) Stop() {
	s.cancel()
	// Wait for Run to fully stop before closing the dispatcher. Skipping the
	// wait would let a concurrent reload close resources the new listeners
	// are still using.
	<-s.started
	<-s.done
	s.dispatcher.Close()
}

func (s *Server) httpHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.httpToken != "" && isProtectedHTTPPath(r.URL.Path) && !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="overture"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		s.HTTPMux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	expected := "Bearer " + s.httpToken
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) == 1
}

func isProtectedHTTPPath(path string) bool {
	return path == "/cache" || path == "/config" || path == "/reload" ||
		path == "/healthz" || path == "/metrics" ||
		strings.HasPrefix(path, "/reload/") || strings.HasPrefix(path, "/debug/pprof")
}

func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CheckBind verifies that address can be bound before a config reload swaps
// the listener. includeUDP controls whether a UDP socket is probed as well;
// the DNS listener needs both TCP and UDP, the debug HTTP listener only TCP.
func CheckBind(address string, includeUDP bool) error {
	if address == "" {
		return nil
	}
	l, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("TCP bind %s: %w", address, err)
	}
	_ = l.Close()
	if includeUDP {
		u, err := net.ListenPacket("udp", address)
		if err != nil {
			return fmt.Errorf("UDP bind %s: %w", address, err)
		}
		_ = u.Close()
	}
	return nil
}

// CheckBindUDP verifies that address can be bound as a UDP socket, used for
// the DoH3 (QUIC) listener which does not need TCP.
func CheckBindUDP(address string) error {
	if address == "" {
		return nil
	}
	u, err := net.ListenPacket("udp", address)
	if err != nil {
		return fmt.Errorf("UDP bind %s: %w", address, err)
	}
	_ = u.Close()
	return nil
}

func (s *Server) ServeDNS(w dns.ResponseWriter, q *dns.Msg) {
	inboundIP, _, _ := net.SplitHostPort(w.RemoteAddr().String())
	metrics.DNSQueriesTotal.WithLabelValues(w.RemoteAddr().Network()).Inc()

	if len(q.Question) == 0 {
		// A QDCOUNT=0 datagram used to panic on Question[0] (the DoH path
		// already rejected this with 400). Answer FORMERR instead.
		m := new(dns.Msg)
		m.SetRcode(q, dns.RcodeFormatError)
		_ = w.WriteMsg(m)
		return
	}

	log.Debugf("Question from %s: %s", inboundIP, q.Question[0].String())

	if q.Opcode != dns.OpcodeQuery {
		// NOTIFY and other non-query opcodes are not supported.
		m := new(dns.Msg)
		m.SetRcode(q, dns.RcodeNotImplemented)
		_ = w.WriteMsg(m)
		return
	}

	for _, qt := range s.rejectQType {
		if isQuestionType(q, qt) {
			log.Debugf("Reject %s: %s", inboundIP, q.Question[0].String())
			dns.HandleFailed(w, q)
			return
		}
	}

	responseMessage := s.dispatcher.Exchange(q, inboundIP)

	if responseMessage == nil {
		dns.HandleFailed(w, q)
		return
	}

	// Unpack clears Compress, and only the cache-hit path used to re-enable
	// it; without this a large response could outgrow the client's EDNS0
	// buffer after passing through overture.
	responseMessage.Compress = true

	metrics.DNSResponsesTotal.WithLabelValues(dns.RcodeToString[responseMessage.Rcode]).Inc()

	err := w.WriteMsg(responseMessage)
	if err != nil {
		log.Warnf("Write message failed, message: %s, error: %s", responseMessage, err)
		return
	}
}

func isDNSMessageContentType(h string) bool {
	if h == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(h)
	return err == nil && strings.EqualFold(mt, doh.MimeType)
}

// dohCacheControl implements RFC 8484 cacheability: successful / negative
// answers advertise an integer max-age (RFC 7234 delta-seconds) taken from
// the smallest record TTL; SERVFAIL and other errors are not HTTP-cached.
func dohCacheControl(msg *dns.Msg) string {
	mt, _ := response.Typify(msg, time.Now().UTC())
	switch mt {
	case response.NoError, response.NameError, response.NoData, response.Delegation:
		age := int(dnsutil.MinimalTTL(msg, mt).Seconds())
		if age < 0 {
			age = 0
		}
		return fmt.Sprintf("max-age=%d", age)
	default:
		return "no-store"
	}
}

func isQuestionType(q *dns.Msg, qt uint16) bool { return q.Question[0].Qtype == qt }
