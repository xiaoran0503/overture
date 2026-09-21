package resolver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

const maxDNSMessageSize = 65535

type HTTPSResolver struct {
	BaseResolver
	client http.Client
}

func (r *HTTPSResolver) Exchange(query *dns.Msg) (*dns.Msg, error) {
	request, err := query.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack DNS query: %w", err)
	}
	response, err := r.client.Post(r.dnsUpstream.Address, "application/dns-message", bytes.NewReader(request))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DoH server returned HTTP %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDNSMessageSize+1))
	if err != nil {
		return nil, fmt.Errorf("read DoH response: %w", err)
	}
	if len(data) > maxDNSMessageSize {
		return nil, fmt.Errorf("DoH response exceeds %d bytes", maxDNSMessageSize)
	}
	message := new(dns.Msg)
	if err := message.Unpack(data); err != nil {
		return nil, fmt.Errorf("unpack DoH response: %w", err)
	}
	return message, nil
}

func (r *HTTPSResolver) Init() error {
	if err := r.BaseResolver.Init(); err != nil {
		return err
	}
	timeout := time.Duration(r.dnsUpstream.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	r.client = http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return r.CreateBaseConn()
			},
			TLSHandshakeTimeout:   timeout,
			ResponseHeaderTimeout: timeout,
		},
	}
	return nil
}

func (r *HTTPSResolver) Close() error {
	r.client.CloseIdleConnections()
	return nil
}
