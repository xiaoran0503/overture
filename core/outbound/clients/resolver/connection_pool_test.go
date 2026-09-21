package resolver

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionPoolReusesAndExpiresConnections(t *testing.T) {
	var created atomic.Int32
	pool, err := newConnectionPool(func() (net.Conn, error) {
		created.Add(1)
		client, _ := net.Pipe()
		return client, nil
	}, 1, 2, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	first, err := pool.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	pool.Release(first)
	time.Sleep(2 * time.Millisecond)
	second, err := pool.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if first == second || created.Load() != 2 {
		t.Fatalf("expired connection was reused; created=%d", created.Load())
	}
}

func TestConnectionPoolCloseUnblocksWaiters(t *testing.T) {
	pool, err := newConnectionPool(func() (net.Conn, error) {
		client, _ := net.Pipe()
		return client, nil
	}, 0, 1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pool.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	result := make(chan error, 1)
	go func() {
		_, err := pool.Acquire()
		result <- err
	}()
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != errConnectionPoolClosed {
			t.Fatalf("waiter error = %v, want pool closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool close did not unblock waiter")
	}
}
