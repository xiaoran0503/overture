/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */
package resolver

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
)

// HTTPS3Resolver is a DNS-over-HTTPS client that speaks HTTP/3 (QUIC) to the
// upstream: same wire format as DoH (application/dns-message POST), but the
// request goes over UDP/QUIC instead of TCP/TLS. Several public DoH services
// (Cloudflare cloudflare-dns.com, Alibaba dns.alidns.com) negotiate h3 on
// their standard :443 endpoint.
type HTTPS3Resolver struct {
	BaseResolver

	client    http.Client
	transport *http3.Transport

	// testTLSClientConfig is a test-only injection point; nil in production.
	testTLSClientConfig *tls.Config
}

func (r *HTTPS3Resolver) Exchange(query *dns.Msg) (*dns.Msg, error) {
	request, err := query.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack DoH3 query: %w", err)
	}
	response, err := r.client.Post(r.dnsUpstream.Address, "application/dns-message", bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DoH3 server returned HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDNSMessageSize+1))
	if err != nil {
		return nil, fmt.Errorf("read DoH3 response: %w", err)
	}
	if len(data) > maxDNSMessageSize {
		return nil, fmt.Errorf("DoH3 response exceeds %d bytes", maxDNSMessageSize)
	}
	message := new(dns.Msg)
	if err := message.Unpack(data); err != nil {
		return nil, fmt.Errorf("unpack DoH3 response: %w", err)
	}
	return message, nil
}

func (r *HTTPS3Resolver) Init() error {
	if err := r.BaseResolver.Init(); err != nil {
		return err
	}
	timeout := time.Duration(r.dnsUpstream.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	tlsConf := r.testTLSClientConfig
	if tlsConf == nil {
		u, err := url.Parse(r.dnsUpstream.Address)
		if err != nil || u.Hostname() == "" {
			return fmt.Errorf("invalid DoH3 address %q", r.dnsUpstream.Address)
		}
		tlsConf = &tls.Config{
			ServerName: u.Hostname(),
			NextProtos: []string{"h3"},
		}
	}
	if len(tlsConf.NextProtos) == 0 {
		tlsConf = tlsConf.Clone()
		tlsConf.NextProtos = []string{"h3"}
	}

	r.transport = &http3.Transport{TLSClientConfig: tlsConf}
	r.client = http.Client{Transport: r.transport, Timeout: timeout}
	return nil
}

func (r *HTTPS3Resolver) Close() error {
	if r.transport != nil {
		r.transport.Close()
	}
	return nil
}

// Compile-time assertion: HTTPS3Resolver implements the upstream Resolver
// contract used by clients.
var _ Resolver = (*HTTPS3Resolver)(nil)
