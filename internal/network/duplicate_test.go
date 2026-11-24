package network

import (
	"errors"
	"io"
	"syscall"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// TestDrainDuplicatesNeverLeavesAStaleSocket pins the fix for a replay seen
// live: PowerToys dialed back while an outbound session was active and sent
// every packet on both sockets. The duplicate sat unread in the accept queue
// for half an hour, and the next session started on it and replayed all of
// it. A duplicate must be read while the session runs, closed when it ends,
// and never handed to the next session.
func TestDrainDuplicatesNeverLeavesAStaleSocket(t *testing.T) {
	dup, peer := secureTestPair(t, t.Name())
	incoming := make(chan *Conn, 1)
	incoming <- dup
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		DrainDuplicates(incoming, stop)
		close(done)
	}()

	// The peer keeps sending on its socket for the whole session.
	pkt := &protocol.Packet{Type: protocol.HeartbeatEx, Src: peer.MachineID, Des: protocol.IDAll}
	pkt.SetMachineName("windows")
	for range 100 {
		if err := peer.SendPacket(pkt); err != nil {
			t.Fatal(err)
		}
	}

	close(stop) // the session ends
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DrainDuplicates did not return after stop")
	}
	select {
	case c := <-incoming:
		t.Fatalf("duplicate left queued for the next session: %v", c)
	default:
	}
	// EOF, or a reset when the close found unread bytes: either way, closed.
	if _, err := peer.RecvPacket(); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("duplicate still open after the session ended: %v", err)
	}
}

// TestDrainDuplicatesClosesOneQueuedAtSessionEnd covers a duplicate that
// completes as the session ends: it is still closed, not left for the next
// session to start on.
func TestDrainDuplicatesClosesOneQueuedAtSessionEnd(t *testing.T) {
	dup, peer := secureTestPair(t, t.Name())
	incoming := make(chan *Conn, 1)
	stop := make(chan struct{})
	close(stop)
	incoming <- dup
	DrainDuplicates(incoming, stop)
	select {
	case c := <-incoming:
		t.Fatalf("duplicate left queued for the next session: %v", c)
	default:
	}
	if _, err := peer.RecvPacket(); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("duplicate still open after the session ended: %v", err)
	}
}

// TestDrainDuplicatesStopsWithClosedListener covers shutdown: the listener
// closes its channel, and draining must still end at stop without spinning.
func TestDrainDuplicatesStopsWithClosedListener(t *testing.T) {
	incoming := make(chan *Conn)
	close(incoming)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		DrainDuplicates(incoming, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DrainDuplicates did not return after stop")
	}
}
