package resolver

import (
	"fmt"
	"net"

	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

type TCPResolver struct {
	BaseResolver
	poolConn *connectionPool
}

func (r *TCPResolver) Close() error {
	if r.poolConn != nil {
		return r.poolConn.Close()
	}
	return nil
}

func (r *TCPResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	if r.dnsUpstream.TCPPoolConfig.Enable {
		if r.poolConn == nil {
			return nil, fmt.Errorf("TCP connection pool is not initialized")
		}
		return r.BaseResolver.exchangeByPool(q, r.poolConn)
	} else {
		return r.BaseResolver.Exchange(q)
	}
}

func (r *TCPResolver) Init() error {
	err := r.BaseResolver.Init()
	if err != nil {
		return err
	}
	if r.dnsUpstream.TCPPoolConfig.Enable {
		r.poolConn, err = r.createConnectionPool(
			func() (net.Conn, error) { return r.CreateBaseConn() })
		if err != nil {
			log.Warnf("Failed to create TCP connection pool for %s: %s", r.dnsUpstream.Name, err)
		}
	} else {
		return nil
	}
	return err
}
