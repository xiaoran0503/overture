package core

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shawn1m/overture/core/config"
)

func buildQuery(name string) ([]byte, error) {
	header := make([]byte, 12)
	binary.BigEndian.PutUint16(header[0:2], 0x1234) // ID
	binary.BigEndian.PutUint16(header[2:4], 0x0100) // RD
	binary.BigEndian.PutUint16(header[4:6], 1)      // QDCOUNT
	labels := []byte{}
	rest := name
	for {
		idx := 0
		for idx < len(rest) && rest[idx] != '.' {
			idx++
		}
		if idx == 0 {
			break
		}
		labels = append(labels, byte(idx))
		labels = append(labels, rest[:idx]...)
		if idx >= len(rest) {
			break
		}
		rest = rest[idx+1:]
	}
	labels = append(labels, 0)
	question := make([]byte, 4)
	binary.BigEndian.PutUint16(question[0:2], 1) // QTYPE A
	binary.BigEndian.PutUint16(question[2:4], 1) // QCLASS IN
	return append(append(header, labels...), question...), nil
}

func writeTestConfig(t *testing.T, dir string, name string, dnsPort int, httpPort int) string {
	t.Helper()
	hostsPath := filepath.Join(dir, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`bindAddress: 127.0.0.1:%d
debugHTTPAddress: 127.0.0.1:%d
debugHTTPToken:
dohEnabled: false
primaryDNS:
  - name: dummy
    address: 127.0.0.1:1
    protocol: udp
    timeout: 1
alternativeDNS: []
onlyPrimaryDNS: true
ipv6UseAlternativeDNS: false
alternativeDNSConcurrent: false
whenPrimaryDNSAnswerNoneUse: primaryDNS
ipNetworkFile:
  primary:
  alternative:
domainFile:
  primary:
  alternative:
  matcher: full-map
hostsFile:
  hostsFile: %s
  finder: full-map
minimumTTL: 60
domainTTLFile:
cacheSize: 0
cacheRedisUrl:
rejectQType: []
`, dnsPort, httpPort, hostsPath)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func queryUDP(t *testing.T, port int) bool {
	t.Helper()
	conn, err := net.DialTimeout("udp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	q, err := buildQuery("example.com.")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(q); err != nil {
		return false
	}
	buf := make([]byte, 512)
	n, err := conn.Read(buf)
	if err != nil {
		return false
	}
	// QR bit must be set and at least one answer record present.
	return n > 12 && buf[2]&0x80 != 0 && (uint16(buf[6])<<8|uint16(buf[7])) >= 1
}

// TestReloadRollsBackOnStartupFailure simulates a reload whose new bind
// address passes the pre-check but is occupied before the real bind (TOCTOU):
// the control layer must roll back to the last known-good configuration and
// keep serving on the original port instead of going silent.
func TestReloadRollsBackOnStartupFailure(t *testing.T) {
	dir := t.TempDir()
	cfgA := writeTestConfig(t, dir, "a.yml", 15557, 15559)

	InitServer(cfgA)
	defer Stop()

	deadline := time.Now().Add(10 * time.Second)
	for !queryUDP(t, 15557) {
		if time.Now().After(deadline) {
			t.Fatal("initial server did not answer on 127.0.0.1:15557")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Occupy the target port so the reloaded server cannot bind it.
	blocker, err := net.Listen("tcp", "127.0.0.1:15558")
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()

	cfgB := writeTestConfig(t, dir, "b.yml", 15558, 15560)
	b, err := config.Load(cfgB)
	if err != nil {
		t.Fatal(err)
	}
	reloadWithConfig(b)

	deadline = time.Now().Add(15 * time.Second)
	for {
		if queryUDP(t, 15557) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rollback did not restore DNS on the original port 15557")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The occupied port must not have been served at any point.
	if queryUDP(t, 15558) {
		t.Fatal("failed reload port unexpectedly answered a query")
	}
}
