// internal/network/bind.go
package network

import (
	"net"
	"strconv"
	"sync/atomic"
	"time"
)

// bindIP restricts mwb to one local address: listeners accept only on it and
// outbound connections leave from it. nil, the default, means every
// interface, with the kernel choosing the outgoing one.
var bindIP atomic.Pointer[net.IP]

// SetBindAddress restricts listening and dialing to ip; nil allows all.
func SetBindAddress(ip net.IP) {
	if ip == nil {
		bindIP.Store(nil)
		return
	}
	bindIP.Store(&ip)
}

// BindAddress returns the address set by SetBindAddress, or nil.
func BindAddress() net.IP {
	if p := bindIP.Load(); p != nil {
		return *p
	}
	return nil
}

// listenAddr is where to listen on port.
func listenAddr(port int) string {
	if ip := BindAddress(); ip != nil {
		return net.JoinHostPort(ip.String(), strconv.Itoa(port))
	}
	return ":" + strconv.Itoa(port)
}

// dial opens a TCP connection, from the bind address when one is set.
func dial(addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	if ip := BindAddress(); ip != nil {
		d.LocalAddr = &net.TCPAddr{IP: ip}
	}
	return d.Dial("tcp", addr)
}
