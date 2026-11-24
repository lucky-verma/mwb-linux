package network

import (
	"io"
	"log/slog"
)

// DrainDuplicates services connections that arrive on incoming while another
// control connection is active, until stop is closed, then closes them.
//
// PowerToys dials back while mwb dials out, and with one machine ID per
// process it sends every packet on each socket it holds to this machine. A
// second connection therefore carries nothing the active one lacks. Left
// queued and unread, it buffers the whole session, and the next session would
// start on it and replay all of it: old clipboard contents, and keystrokes if
// any were typed. Reading it keeps its buffers empty; closing it when the
// session ends means it is never resumed.
func DrainDuplicates(incoming <-chan *Conn, stop <-chan struct{}) {
	var dups []*Conn
	defer func() {
		for _, c := range dups {
			_ = c.Close()
		}
	}()
	for {
		select {
		case c, ok := <-incoming:
			if !ok {
				incoming = nil // listener stopped; wait for the session to end
				continue
			}
			if c == nil {
				continue
			}
			slog.Info("second connection from the peer; discarding it until this session ends",
				"remote", c.RemoteName)
			dups = append(dups, c)
			go func() { _, _ = io.Copy(io.Discard, c.raw) }()
		case <-stop:
			// One that completed as the session ended is just as stale.
			for {
				select {
				case c, ok := <-incoming:
					if !ok {
						return
					}
					if c != nil {
						dups = append(dups, c)
					}
				default:
					return
				}
			}
		}
	}
}
