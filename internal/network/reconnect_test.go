package network

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

func secureTestPair(t *testing.T, name string) (*Conn, *Conn) {
	t.Helper()
	const key = "ReconnectTestKey!"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type result struct {
		conn *Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			accepted <- result{err: err}
			return
		}
		conn, err := newSecureStream(raw, key, "reconnect-test-peer", 5*time.Second)
		if err != nil {
			_ = raw.Close()
		}
		accepted <- result{conn: conn, err: err}
	}()

	raw, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	conn, err := newSecureStream(raw, key, name, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	if peer.err != nil {
		t.Fatal(peer.err)
	}
	t.Cleanup(func() { _ = peer.conn.raw.Close() })
	return conn, peer.conn
}

func TestReconnectKeepsMachineIdentity(t *testing.T) {
	first, _ := secureTestPair(t, t.Name())
	second, _ := secureTestPair(t, t.Name())
	if first.MachineID != second.MachineID {
		t.Fatalf("same machine advertised different IDs on competing connections: %d and %d",
			first.MachineID, second.MachineID)
	}
}

func TestReconnectDoesNotReusePacketIDs(t *testing.T) {
	seen := make(map[int32]bool)
	for range 2 {
		conn, peer := secureTestPair(t, t.Name())
		pkt := &protocol.Packet{Type: protocol.HeartbeatEx, Src: conn.MachineID, Des: protocol.IDAll}
		pkt.SetMachineName(t.Name())
		if err := conn.SendPacket(pkt); err != nil {
			t.Fatal(err)
		}
		received, err := peer.RecvPacket()
		if err != nil {
			t.Fatal(err)
		}
		// PowerToys' receive-side deduplication spans all sockets.
		if seen[received.ID] {
			t.Fatalf("reconnect reused packet ID %d; the peer would discard the registration heartbeat", received.ID)
		}
		seen[received.ID] = true
	}
}

func TestCancelledConnectDoesNotAnnounceMachineDeparture(t *testing.T) {
	conn, peer := secureTestPair(t, t.Name())
	stop := make(chan struct{})
	started := make(chan struct{})
	finish := make(chan struct{})
	conns := connectWithRetry(stop, time.Hour, func() (*Conn, error) {
		close(started)
		<-finish
		return conn, nil
	})
	<-started
	close(stop) // The inbound connection won while the outbound dial was in flight.
	close(finish)

	pkt, err := peer.RecvPacket()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("losing socket should close without a machine-wide ByeBye; packet=%v, error=%v", pkt, err)
	}
	select {
	case _, ok := <-conns:
		if ok {
			t.Fatal("cancelled connection was handed to the caller")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled dial did not finish")
	}
}

func TestCancelledConnectDoesNotStartAnotherDial(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	conns := connectWithRetry(stop, time.Hour, func() (*Conn, error) {
		t.Error("started a dial after cancellation")
		return nil, errors.New("cancelled")
	})
	select {
	case _, ok := <-conns:
		if ok {
			t.Fatal("returned a connection after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled dial loop did not finish")
	}
}
