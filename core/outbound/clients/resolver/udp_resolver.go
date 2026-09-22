package resolver

import (
	"github.com/miekg/dns"
	log "github.com/sirupsen/logrus"
)

type UDPResolver struct {
	BaseResolver
}

func (r *UDPResolver) Exchange(q *dns.Msg) (*dns.Msg, error) {
	msg, err := r.BaseResolver.Exchange(q)
	if err != nil {
		return nil, err
	}
	if msg != nil && msg.Truncated {
		// RFC 1035 4.2.1: a client receiving a truncated (TC=1) UDP
		// response must retry over TCP. Retry once against the same
		// upstream; CreateBaseConn inherits the SOCKS5 and default-port
		// addressing logic, so the fallback works for proxied setups too.
		log.Debugf("%s returned a truncated UDP response; retrying over TCP", r.dnsUpstream.Name)
		u := *r.dnsUpstream
		u.Protocol = "tcp"
		tcp := &TCPResolver{BaseResolver: BaseResolver{dnsUpstream: &u}}
		return tcp.BaseResolver.Exchange(q)
	}
	return msg, nil
}

func (r *UDPResolver) Init() error {
	err := r.BaseResolver.Init()
	if err != nil {
		return err
	}
	return nil
}
