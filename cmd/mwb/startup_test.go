// cmd/mwb/startup_test.go
package main

import (
	"net"
	"testing"
)

func TestPickInterfaceIPPrefersTheLANAddress(t *testing.T) {
	ip := func(s string) net.IP { return net.ParseIP(s) }
	for _, tc := range []struct {
		in   []net.IP
		want string
	}{
		{[]net.IP{ip("fe80::1"), ip("2001:db8::5"), ip("192.168.1.7")}, "192.168.1.7"},
		{[]net.IP{ip("100.64.0.9"), ip("10.0.0.4")}, "10.0.0.4"},
		{[]net.IP{ip("fe80::1"), ip("100.64.0.9")}, "100.64.0.9"},
		{[]net.IP{ip("fe80::1"), ip("2001:db8::5")}, "2001:db8::5"},
		{[]net.IP{ip("fe80::1"), ip("169.254.3.3")}, "<nil>"},
	} {
		if got := pickInterfaceIP(tc.in).String(); got != tc.want {
			t.Errorf("%v: got %s, want %s", tc.in, got, tc.want)
		}
	}
}
