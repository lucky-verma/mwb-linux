//go:build linux

package network

import (
	"syscall"
	"unsafe"
)

// siocOutqNSD is SIOCOUTQNSD: bytes queued on a TCP socket that have not been
// sent yet (unlike SIOCOUTQ, which also counts sent but unacknowledged ones).
const siocOutqNSD = 0x894B

// Unsent reports how many bytes the kernel holds for this connection that it
// has not put on the network yet; 0 when that can't be read. A growing count
// means the network has stalled, and anything written now waits behind it.
func (c *Conn) Unsent() int {
	sc, ok := c.raw.(syscall.Conn)
	if !ok {
		return 0
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0
	}
	var n int32
	_ = raw.Control(func(fd uintptr) {
		if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, siocOutqNSD, uintptr(unsafe.Pointer(&n))); e != 0 {
			n = 0
		}
	})
	return int(n)
}
