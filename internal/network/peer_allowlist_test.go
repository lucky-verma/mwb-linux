// internal/network/peer_allowlist_test.go
package network

import (
	"net"
	"testing"
	"time"
)

func TestIsAllowedPeer(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		allowedIPs []net.IP
		want       bool
	}{
		{"matching IP", "192.168.1.50:41000", []net.IP{net.ParseIP("192.168.1.50")}, true},
		{"different IP rejected", "192.168.1.99:41000", []net.IP{net.ParseIP("192.168.1.50")}, false},
		{"loopback not the peer rejected", "127.0.0.1:41000", []net.IP{net.ParseIP("192.168.1.50")}, false},
		{"empty allowlist rejects", "10.0.0.7:41000", nil, false},
		{"IPv6 match", "[fe80::1]:41000", []net.IP{net.ParseIP("fe80::1")}, true},
		{"IPv6 mismatch", "[fe80::2]:41000", []net.IP{net.ParseIP("fe80::1")}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := net.ResolveTCPAddr("tcp", tt.remote)
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.remote, err)
			}
			if got := isAllowedPeer(addr, tt.allowedIPs); got != tt.want {
				t.Errorf("isAllowedPeer(%q) = %v, want %v", tt.remote, got, tt.want)
			}
		})
	}
}

func TestResolveAllowedPeerIPLiteral(t *testing.T) {
	ips, err := resolveAllowedPeer("192.168.1.50")
	if err != nil {
		t.Fatal(err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.ParseIP("192.168.1.50")) {
		t.Fatalf("resolved IPs = %v, want [192.168.1.50]", ips)
	}
}

func TestResolveAllowedPeerRejectsEmptyHost(t *testing.T) {
	if _, err := resolveAllowedPeer(""); err == nil {
		t.Fatal("expected empty host to fail")
	}
}

func TestSetupConnHandshakeDeadline(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()

	start := time.Now()
	if _, err := setupConn(client, "TestSecurityKey!!", "linux-test", 50*time.Millisecond); err == nil {
		t.Fatal("expected stalled handshake to time out")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("stalled handshake took %v, want less than 1s", elapsed)
	}
}
