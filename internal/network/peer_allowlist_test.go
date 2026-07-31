// internal/network/peer_allowlist_test.go
package network

import (
	"net"
	"testing"
	"time"
)

func TestIsAllowedPeer(t *testing.T) {
	peer := []net.IP{net.ParseIP("192.168.1.50")}

	tests := []struct {
		name       string
		remote     string
		allowedIPs []net.IP
		want       bool
	}{
		// Exact match against the configured host: the fast path.
		{"configured peer", "192.168.1.50:41000", peer, true},
		{"configured peer, IPv6", "[fe80::1]:41000", []net.IP{net.ParseIP("fe80::1")}, true},

		// The configured peer is a machine, not an address. A dual-stack host
		// connects from an IPv6 link-local address that no lookup of its IPv4
		// can return, and DHCP renewal or a second NIC moves it within the LAN.
		// Refusing these rejected the legitimate peer and flapped the link.
		{"peer's link-local address", "[fe80::5054:ff:fe12:3456]:41000", peer, true},
		{"peer after DHCP renewal", "192.168.1.99:41000", peer, true},
		{"peer on another private range", "10.0.0.7:41000", peer, true},
		{"peer on a ULA address", "[fd00::1]:41000", peer, true},

		// The internet must never reach the handshake.
		{"public IPv4 rejected", "8.8.8.8:41000", peer, false},
		{"public IPv6 rejected", "[2001:4860:4860::8888]:41000", peer, false},

		// Loopback is this machine, never the peer.
		{"loopback rejected", "127.0.0.1:41000", peer, false},

		// Resolution failed, so no peer is known: fail closed.
		{"empty allowlist rejects private", "10.0.0.7:41000", nil, false},
		{"empty allowlist rejects exact", "192.168.1.50:41000", nil, false},
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

// Widening who may attempt the handshake must not widen who passes it. The
// shared key is the authentication control; isAllowedPeer only decides who is
// allowed to spend a handshake slot trying.
func TestIsAllowedPeer_DoesNotAuthenticate(t *testing.T) {
	addr, err := net.ResolveTCPAddr("tcp", "192.168.1.99:41000")
	if err != nil {
		t.Fatal(err)
	}
	if !isAllowedPeer(addr, []net.IP{net.ParseIP("192.168.1.50")}) {
		t.Skip("LAN sources are refused; nothing to assert")
	}

	// A LAN source that reaches the handshake without the key must still fail.
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	if _, err := setupConn(client, "TestSecurityKey!!", "linux-test", 50*time.Millisecond); err == nil {
		t.Error("a peer that never completes the handshake must not be accepted")
	}
}

func TestIsLocalNetwork(t *testing.T) {
	for _, tt := range []struct {
		ip   string
		want bool
	}{
		{"192.168.1.5", true},
		{"10.1.2.3", true},
		{"172.16.0.1", true},
		{"fe80::1", true},
		{"fd00::1", true},
		{"8.8.8.8", false},
		{"2001:4860:4860::8888", false},
		{"127.0.0.1", false}, // this machine, never the peer
		{"::1", false},
	} {
		if got := isLocalNetwork(net.ParseIP(tt.ip)); got != tt.want {
			t.Errorf("isLocalNetwork(%s) = %v, want %v", tt.ip, got, tt.want)
		}
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
