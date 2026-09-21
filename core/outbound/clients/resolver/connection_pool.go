package resolver

import (
	"errors"
	"net"
	"sync"
	"time"
)

var errConnectionPoolClosed = errors.New("connection pool is closed")

type pooledConnection struct {
	conn      net.Conn
	idleSince time.Time
}

// connectionPool is deliberately limited to net.Conn. It avoids a general-purpose
// dependency while preserving bounded TCP/TLS connection reuse for DNS upstreams.
type connectionPool struct {
	factory     func() (net.Conn, error)
	idleTimeout time.Duration
	idle        chan pooledConnection
	slots       chan struct{}
	done        chan struct{}

	mu     sync.Mutex
	closed bool
}

func newConnectionPool(factory func() (net.Conn, error), initial, maximum int, idleTimeout time.Duration) (*connectionPool, error) {
	if maximum <= 0 {
		maximum = 15
	}
	if initial < 0 {
		initial = 0
	}
	if initial > maximum {
		initial = maximum
	}
	pool := &connectionPool{
		factory:     factory,
		idleTimeout: idleTimeout,
		idle:        make(chan pooledConnection, maximum),
		slots:       make(chan struct{}, maximum),
		done:        make(chan struct{}),
	}
	for range initial {
		connection, err := pool.newConnection()
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
		pool.idle <- pooledConnection{conn: connection, idleSince: time.Now()}
	}
	return pool, nil
}

func (p *connectionPool) Acquire() (net.Conn, error) {
	for {
		if connection, ok := p.takeIdle(); ok {
			if p.expired(connection) {
				p.discard(connection.conn)
				continue
			}
			return connection.conn, nil
		}

		select {
		case p.slots <- struct{}{}:
			connection, err := p.factory()
			if err != nil {
				<-p.slots
				return nil, err
			}
			if p.isClosed() {
				p.discard(connection)
				return nil, errConnectionPoolClosed
			}
			return connection, nil
		case connection := <-p.idle:
			if p.expired(connection) {
				p.discard(connection.conn)
				continue
			}
			return connection.conn, nil
		case <-p.done:
			return nil, errConnectionPoolClosed
		}
	}
}

func (p *connectionPool) Release(connection net.Conn) {
	if connection == nil {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.discard(connection)
		return
	}
	select {
	case p.idle <- pooledConnection{conn: connection, idleSince: time.Now()}:
		p.mu.Unlock()
	default:
		p.mu.Unlock()
		p.discard(connection)
	}
}

func (p *connectionPool) discard(connection net.Conn) {
	if connection != nil {
		_ = connection.Close()
	}
	select {
	case <-p.slots:
	default:
	}
}

func (p *connectionPool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.done)
	for {
		select {
		case connection := <-p.idle:
			_ = connection.conn.Close()
			<-p.slots
		default:
			p.mu.Unlock()
			return nil
		}
	}
}

func (p *connectionPool) newConnection() (net.Conn, error) {
	p.slots <- struct{}{}
	connection, err := p.factory()
	if err != nil {
		<-p.slots
		return nil, err
	}
	return connection, nil
}

func (p *connectionPool) takeIdle() (pooledConnection, bool) {
	if p.isClosed() {
		return pooledConnection{}, false
	}
	select {
	case connection := <-p.idle:
		return connection, true
	default:
		return pooledConnection{}, false
	}
}

func (p *connectionPool) expired(connection pooledConnection) bool {
	return p.idleTimeout > 0 && time.Since(connection.idleSince) >= p.idleTimeout
}

func (p *connectionPool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}
