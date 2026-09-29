/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */
package resolver

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go"
	log "github.com/sirupsen/logrus"
)

const doqALPN = "doq"

// QUICResolver is a DNS-over-QUIC (RFC 9250) client resolver. One QUIC
// connection carries many queries, each on its own stream; the connection is
// established lazily on first use and re-established transparently after
// failure.
type QUICResolver struct {
	BaseResolver

	mu   sync.Mutex
	conn *quic.Conn

	// testTLSConfig is a test-only injection point; nil in production.
	testTLSConfig *tls.Config
}

func (r *QUICResolver) Init() error { return nil }

func (r *QUICResolver) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn != nil {
		r.conn.CloseWithError(0, "close")
		r.conn = nil
	}
	return nil
}

// parseDoQAddress accepts doq:// or quic:// prefixes (the latter matches the
// scheme AdGuard/Alibaba HTTPDNS use), an optional SNI via "sni@host", and a
// default port of 853.
func parseDoQAddress(raw string) (host string, port string, sni string, err error) {
	s := strings.TrimPrefix(raw, "doq://")
	s = strings.TrimPrefix(s, "quic://")

	if at := strings.LastIndex(s, "@"); at >= 0 {
		sni = s[:at]
		s = s[at+1:]
	}
	if h, p, e := net.SplitHostPort(s); e == nil {
		host, port = h, p
	} else {
		host = s
		port = "853"
	}
	if host == "" {
		return "", "", "", errors.New("doq address has empty host")
	}
	if port == "" {
		port = "853"
	}
	if sni == "" {
		if net.ParseIP(host) != nil {
			return "", "", "", fmt.Errorf("doq address %q: IP upstream requires SNI, use doq://sni@%s", raw, host)
		}
		sni = host
	}
	return host, port, sni, nil
}

func (r *QUICResolver) dropConn(conn *quic.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == conn {
		r.conn.CloseWithError(0, "reconnect")
		r.conn = nil
	}
}

func (r *QUICResolver) getConn(ctx context.Context) (*quic.Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn != nil {
		select {
		case <-r.conn.Context().Done():
			r.conn = nil
		default:
			return r.conn, nil
		}
	}

	host, port, sni, err := parseDoQAddress(r.dnsUpstream.Address)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(r.dnsUpstream.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	tlsConf := r.testTLSConfig
	if tlsConf == nil {
		tlsConf = &tls.Config{
			ServerName: sni,
			NextProtos: []string{doqALPN},
			MinVersion: tls.VersionTLS13,
		}
	}
	if len(tlsConf.NextProtos) == 0 {
		tlsConf = tlsConf.Clone()
		tlsConf.NextProtos = []string{doqALPN}
	}

	conn, err := quic.DialAddr(ctx, net.JoinHostPort(host, port), tlsConf, &quic.Config{
		MaxIdleTimeout: timeout,
	})
	if err != nil {
		log.Warnf("DoQ dial %s failed: %s", r.dnsUpstream.Name, err)
		return nil, err
	}
	r.conn = conn
	return conn, nil
}

// Exchange performs one DNS query over its own QUIC stream per RFC 9250:
// write a 2-byte big-endian length prefix, then the packed message, half-close
// the stream, read the 2-byte prefixed response, and validate the ID.
func (r *QUICResolver) Exchange(query *dns.Msg) (*dns.Msg, error) {
	timeout := time.Duration(r.dnsUpstream.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := r.getConn(ctx)
	if err != nil {
		return nil, err
	}

	msg, err := query.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack DoQ query: %w", err)
	}
	if len(msg) > 65535 {
		return nil, errors.New("DoQ query exceeds 65535 bytes")
	}

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		// The cached connection died between getConn and OpenStream; drop it
		// and retry once with a fresh connection.
		r.dropConn(conn)
		if conn, err = r.getConn(ctx); err != nil {
			return nil, err
		}
		if stream, err = conn.OpenStreamSync(ctx); err != nil {
			return nil, err
		}
	}
	defer stream.Close()

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(msg)))
	if _, err := stream.Write(lenBuf[:]); err != nil {
		return nil, fmt.Errorf("DoQ write length: %w", err)
	}
	if _, err := stream.Write(msg); err != nil {
		return nil, fmt.Errorf("DoQ write query: %w", err)
	}
	// Half-close the write side; the server replies on the same stream.
	if err := stream.Close(); err != nil {
		return nil, fmt.Errorf("DoQ close write side: %w", err)
	}

	deadline := time.Now().Add(timeout)
	stream.SetReadDeadline(deadline)

	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("DoQ read response length: %w", err)
	}
	length := binary.BigEndian.Uint16(lenBuf[:])
	if length == 0 {
		return nil, errors.New("DoQ empty response")
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(stream, data); err != nil {
		return nil, fmt.Errorf("DoQ read response: %w", err)
	}

	resp := new(dns.Msg)
	if err := resp.Unpack(data); err != nil {
		return nil, fmt.Errorf("unpack DoQ response: %w", err)
	}
	if resp.Id != query.Id {
		return nil, fmt.Errorf("DoQ response ID mismatch (sent %d, got %d)", query.Id, resp.Id)
	}
	return resp, nil
}
