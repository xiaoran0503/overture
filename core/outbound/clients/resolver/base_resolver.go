/*
 * Copyright (c) 2019 shawn1m. All rights reserved.
 * Use of this source code is governed by The MIT License (MIT) that can be
 * found in the LICENSE file..
 */
package resolver

import (
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
	"net"
	"time"

	"github.com/shawn1m/overture/core/common"
)

type Resolver interface {
	Exchange(*dns.Msg) (*dns.Msg, error)
	Init() error
	Close() error
}

type BaseResolver struct {
	dnsUpstream *common.DNSUpstream
}

func (r *BaseResolver) Close() error { return nil }

func (r *BaseResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	conn, err := r.CreateBaseConn()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return r.exchangeByConnWithoutClose(q, conn)
}

func (r *BaseResolver) exchangeByConnWithoutClose(q *dns.Msg, conn net.Conn) (msg *dns.Msg, err error) {
	if conn == nil {
		log.Fatal("Conn not initialized for exchangeByDNSClient")
		return nil, err
	}

	r.setTimeout(conn)
	dc := &dns.Conn{Conn: conn, UDPSize: 65535}
	err = dc.WriteMsg(q)
	if err != nil {
		log.Warnf("%s Fail: Send question message failed", r.dnsUpstream.Name)
		return nil, err
	}
	return dc.ReadMsg()
}

func (r *BaseResolver) Init() error {
	return nil
}

func NewResolver(u *common.DNSUpstream) Resolver {
	var resolver Resolver
	switch u.Protocol {
	case "udp":
		resolver = &UDPResolver{BaseResolver: BaseResolver{u}}
	case "tcp":
		resolver = &TCPResolver{BaseResolver: BaseResolver{u}}
	case "tcp-tls":
		resolver = &TCPTLSResolver{BaseResolver: BaseResolver{u}}
	case "https":
		resolver = &HTTPSResolver{BaseResolver: BaseResolver{u}}
	default:
		log.Fatalf("Unsupported protocol: %s", u.Protocol)
		log.Errorf("Create resolver for %s failed", u.Name)
		return nil
	}
	err := resolver.Init()
	if err != nil {
		log.Errorf("Init resolver for %s failed", u.Name)
	} else {
		log.Debugf("Init resolver for %s succeed", u.Name)
	}
	return resolver
}

func (r *BaseResolver) CreateBaseConn() (net.Conn, error) {
	dialer := net.Dialer{Timeout: r.getDialTimeout()}
	dialerFunc := dialer.Dial
	if r.dnsUpstream.SOCKS5Address != "" {
		socksAddress, err := ExtractFullUrl(r.dnsUpstream.SOCKS5Address, "socks5")
		if err != nil {
			return nil, err
		}
		network := ToNetwork(r.dnsUpstream.Protocol)
		s, err := proxy.SOCKS5(network, socksAddress, nil, proxy.Direct)
		if err != nil {
			log.Warnf("Failed to connect to SOCKS5 proxy: %s", err)
			return nil, err
		}
		dialerFunc = s.Dial
	}

	network := ToNetwork(r.dnsUpstream.Protocol)
	host, port, err := ExtractDNSAddress(r.dnsUpstream.Address, r.dnsUpstream.Protocol)
	if err != nil {
		return nil, err
	}
	address := net.JoinHostPort(host, port)
	log.Debugf("Creating new connection to %s:%s", host, port)
	var conn net.Conn
	if conn, err = dialerFunc(network, address); err != nil {
		log.Warnf("Failed to connect to DNS upstream: %s", err)
		return nil, err
	}

	// the Timeout setting is now moved to each resolver to support pool's idle timeout
	// r.setTimeout(conn)
	return conn, err
}

func (r *BaseResolver) setTimeout(conn net.Conn) {
	dnsTimeout := time.Duration(r.dnsUpstream.Timeout) * time.Second / 3
	conn.SetDeadline(time.Now().Add(dnsTimeout))
	conn.SetReadDeadline(time.Now().Add(dnsTimeout))
	conn.SetWriteDeadline(time.Now().Add(dnsTimeout))
}

func (r *BaseResolver) getDialTimeout() time.Duration {
	return time.Duration(r.dnsUpstream.Timeout) * time.Second / 3
}

func (r *BaseResolver) createConnectionPool(connCreate func() (net.Conn, error)) (*connectionPool, error) {
	config := r.dnsUpstream.TCPPoolConfig
	idleTimeout := 30 * time.Second
	if config.IdleTimeout > 0 {
		idleTimeout = time.Duration(config.IdleTimeout) * time.Second
	}
	maximum := config.MaxCapacity
	if maximum == 0 {
		maximum = 15
	}
	return newConnectionPool(connCreate, config.InitialCapacity, maximum, idleTimeout)
}

func (r *BaseResolver) exchangeByPool(q *dns.Msg, poolConn *connectionPool) (msg *dns.Msg, err error) {
	conn, err := poolConn.Acquire()
	if err != nil {
		return nil, err
	}
	ret, err := r.exchangeByConnWithoutClose(q, conn)
	if err != nil {
		poolConn.discard(conn)
	} else {
		poolConn.Release(conn)
	}
	return ret, err
}
