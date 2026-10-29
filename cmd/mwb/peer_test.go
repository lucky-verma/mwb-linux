// cmd/mwb/peer_test.go
package main

import (
	"net"
	"reflect"
	"testing"
)

func TestLocalAddressStrings(t *testing.T) {
	cidr := func(s string) net.Addr {
		ip, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatal(err)
		}
		n.IP = ip
		return n
	}
	got := localAddressStrings([]net.Addr{
		cidr("127.0.0.1/8"), cidr("10.0.0.20/24"), cidr("192.0.2.2/24"),
		cidr("fe80::1/64"), cidr("fd00::5/64"), cidr("8.8.8.8/32"),
	})
	want := []string{"10.0.0.20", "fd00::5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localAddressStrings = %v, want %v", got, want)
	}
}

func TestPeerHostFallsBackToConfigured(t *testing.T) {
	if got := peerHost("10.0.0.2", nil); got != "10.0.0.2" {
		t.Fatalf("peerHost = %q", got)
	}
}
