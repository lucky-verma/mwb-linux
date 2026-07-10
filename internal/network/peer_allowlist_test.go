// internal/network/peer_allowlist_test.go
package network

import (
	"net"
	"testing"
)

func TestIsAllowedPeer(t *testing.T) {
	tests := []struct {
		name        string
		remote      string // remote address "ip:port"
		allowedHost string
		want        bool
	}{
		{"matching IP", "192.168.1.50:41000", "192.168.1.50", true},
		{"different IP rejected", "192.168.1.99:41000", "192.168.1.50", false},
		{"loopback not the peer rejected", "127.0.0.1:41000", "192.168.1.50", false},
		{"empty allowedHost disables check", "10.0.0.7:41000", "", true},
		{"IPv6 match", "[fe80::1]:41000", "fe80::1", true},
		{"IPv6 mismatch", "[fe80::2]:41000", "fe80::1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := net.ResolveTCPAddr("tcp", tt.remote)
			if err != nil {
				t.Fatalf("resolve %q: %v", tt.remote, err)
			}
			if got := isAllowedPeer(addr, tt.allowedHost); got != tt.want {
				t.Errorf("isAllowedPeer(%q, %q) = %v, want %v",
					tt.remote, tt.allowedHost, got, tt.want)
			}
		})
	}
}
