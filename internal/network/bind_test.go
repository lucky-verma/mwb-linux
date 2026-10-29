// internal/network/bind_test.go
package network

import (
	"net"
	"testing"
	"time"
)

func TestBindAddressRestrictsListenAndDial(t *testing.T) {
	t.Cleanup(func() { SetBindAddress(nil) })
	if got := listenAddr(15101); got != ":15101" {
		t.Fatalf("default listen address %q", got)
	}
	SetBindAddress(net.ParseIP("127.0.0.1"))
	if got := listenAddr(15101); got != "127.0.0.1:15101" {
		t.Fatalf("bound listen address %q", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	c, err := dial(ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if ip := c.LocalAddr().(*net.TCPAddr).IP; !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("dialed from %v", ip)
	}
}
