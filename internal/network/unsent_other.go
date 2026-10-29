//go:build !linux

package network

// Unsent is not measured on this platform.
func (c *Conn) Unsent() int { return 0 }
