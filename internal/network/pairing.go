// internal/network/pairing.go
package network

import (
	"log/slog"
	"strings"
	"time"
)

// Linux-peer pairing.
//
// Both ends listen and dial at the same time, and against PowerToys the first
// authenticated connection wins: Windows keeps one socket per machine and
// closes the duplicate itself. Two mwb-linux instances have no such referee.
// Each can keep a different socket — A its outbound, B its own outbound — and
// because neither ever reads the socket the other is using, both wait forever
// while input is written into a connection nobody reads.
//
// SelectLinuxPeer removes that ambiguity with a rule both ends evaluate the
// same way: keep the connection dialed by the machine whose name sorts first.
// That connection is A's outbound and B's inbound, so both pick one socket.
// The rule is a preference, not a requirement. If the preferred direction
// cannot be established (one machine firewalled, say), the other connection is
// still used after a short grace period, which is also the single connection
// the other end ends up holding.

const (
	// LinuxPeerGrace bounds how long pairing waits for the preferred
	// connection once the other one has arrived. Both directions normally
	// complete within a second of each other on a LAN.
	LinuxPeerGrace = 3 * time.Second

	// LinuxPeerIdleTimeout drops a Linux-peer control connection that has
	// been silent this long. Both ends send a heartbeat every second on such
	// a link (see heartbeatEvery) and answer each other's, so a healthy link
	// is never quiet for more than a second or so. When a machine vanishes
	// while it has the cursor, this is how long the others wait before they
	// take their own keyboard and mouse back.
	LinuxPeerIdleTimeout = 5 * time.Second
)

// preferOutbound reports whether the local machine should keep its own
// outbound connection, and whether the names allow a decision at all.
func preferOutbound(localName, remoteName string) (prefer, decidable bool) {
	local := strings.TrimRight(localName, " ")
	remote := strings.TrimRight(remoteName, " ")
	if local == remote {
		return false, false
	}
	return local < remote, true
}

// SelectLinuxPeer waits for the first authenticated control connection from
// either channel and returns the one both mwb-linux peers agree on. The
// connection it does not keep is closed. outbound reports which direction was
// kept.
//
// A nil or closed channel is ignored; if both are closed it returns nil.
func SelectLinuxPeer(outgoing, incoming <-chan *Conn, grace time.Duration) (conn *Conn, outbound bool) {
	first, firstOut, ok := recvEither(outgoing, incoming)
	if !ok {
		return nil, false
	}

	prefer, decidable := preferOutbound(first.LocalName, first.RemoteName)
	if !decidable {
		slog.Warn("peer = linux: both machines use the same name, so pairing cannot be made deterministic; set a different name on one of them",
			"name", first.LocalName)
		return first, firstOut
	}
	if prefer == firstOut {
		return first, firstOut
	}

	other := incoming
	if !firstOut {
		other = outgoing
	}
	if other == nil {
		return first, firstOut
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case c, ok := <-other:
		if !ok || c == nil {
			return first, firstOut
		}
		slog.Info("peer = linux: keeping the connection dialed by the machine whose name sorts first",
			"local", first.LocalName, "remote", first.RemoteName, "outbound", !firstOut)
		_ = first.Close()
		return c, !firstOut
	case <-timer.C:
		slog.Info("peer = linux: preferred connection did not arrive; using the one available",
			"outbound", firstOut, "waited", grace)
		return first, firstOut
	}
}

// recvEither returns the first connection delivered by either channel.
func recvEither(outgoing, incoming <-chan *Conn) (*Conn, bool, bool) {
	for outgoing != nil || incoming != nil {
		select {
		case c, ok := <-outgoing:
			if !ok {
				outgoing = nil
				continue
			}
			if c != nil {
				return c, true, true
			}
		case c, ok := <-incoming:
			if !ok {
				incoming = nil
				continue
			}
			if c != nil {
				return c, false, true
			}
		}
	}
	return nil, false, false
}

// PreferOutbound reports whether the local machine should keep the
// connection it dialed rather than the one the remote dialed, by the same
// rule SelectLinuxPeer uses; decidable is false when the names are equal.
func PreferOutbound(localName, remoteName string) (prefer, decidable bool) {
	return preferOutbound(localName, remoteName)
}
