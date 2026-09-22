package resolver

import (
	"crypto/tls"
	"fmt"
	"net"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

type TCPTLSResolver struct {
	BaseResolver
	poolConn *connectionPool
}

func (r *TCPTLSResolver) Close() error {
	if r.poolConn != nil {
		return r.poolConn.Close()
	}
	return nil
}

func (r *TCPTLSResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	if r.dnsUpstream.TCPPoolConfig.Enable {
		if r.poolConn == nil {
			return nil, fmt.Errorf("TLS connection pool is not initialized")
		}
		return r.BaseResolver.exchangeByPool(q, r.poolConn)
	} else {
		conn, err := r.createTlsConn()
		if err != nil {
			log.Warnf("createTlsConn failed: %s", err)
			return nil, err
		}
		defer conn.Close()
		return r.exchangeByConnWithoutClose(q, conn)
	}
}

func (r *TCPTLSResolver) createTlsConn() (conn net.Conn, err error) {
	conn, err = r.CreateBaseConn()
	if err != nil {
		return nil, err
	}
	host, err := ExtractTLSDNSHostName(r.dnsUpstream.Address)
	if err != nil {
		return nil, err
	}
	conf := &tls.Config{
		InsecureSkipVerify: false,
		ServerName:         host,
	}
	conn = tls.Client(conn, conf)

	return conn, nil
}

func (r *TCPTLSResolver) Init() error {
	err := r.BaseResolver.Init()
	if err != nil {
		return err
	}
	if r.dnsUpstream.TCPPoolConfig.Enable {
		r.poolConn, err = r.createConnectionPool(
			func() (net.Conn, error) { return r.createTlsConn() })
		if err != nil {
			log.Warnf("Failed to create TLS connection pool for %s: %s", r.dnsUpstream.Name, err)
		}
	} else {
		return nil
	}
	return err
}
