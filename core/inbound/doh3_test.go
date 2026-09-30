package inbound

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/quic-go/quic-go/http3"
)

func TestServeDNSHttp3Answers(t *testing.T) {
	s := NewServer("127.0.0.1:53", "127.0.0.1:5555", hostDispatcher(t), nil, false, "")
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	body, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/dns-query", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/dns-message")
	rec := httptest.NewRecorder()
	s.ServeDNSHttp3(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestCheckBindUDP(t *testing.T) {
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.LocalAddr().String()
	if err := CheckBindUDP(addr); err == nil {
		t.Fatal("CheckBindUDP accepted an occupied UDP address")
	}
	_ = ln.Close()
	if err := CheckBindUDP(addr); err != nil {
		t.Fatalf("CheckBindUDP on a free address: %v", err)
	}
}

func TestDoH3HTTP3RoundTrip(t *testing.T) {
	certFile, keyFile := writeTempDoH3Cert(t)
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer("127.0.0.1:53", "", hostDispatcher(t), nil, false, "")
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer udpConn.Close()
	addr := udpConn.LocalAddr().String()

	tlsConf := http3.ConfigureTLSConfig(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	})
	h3mux := http.NewServeMux()
	h3mux.HandleFunc("/dns-query", s.ServeDNSHttp3)
	h3 := &http3.Server{Handler: h3mux, TLSConfig: tlsConf}
	go func() { _ = h3.Serve(udpConn) }()
	defer h3.Close()

	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	packed, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	tr := &http3.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"h3"},
		MinVersion:         tls.VersionTLS13,
	}}
	defer tr.Close()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Post("https://"+addr+"/dns-query", "application/dns-message", bytes.NewReader(packed))
	if err != nil {
		t.Fatalf("DoH3 POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DoH3 status = %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	msg := new(dns.Msg)
	if err := msg.Unpack(raw); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	if len(msg.Answer) != 1 {
		t.Fatalf("answers = %d, want 1", len(msg.Answer))
	}
}

func writeTempDoH3Cert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "doh3.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
