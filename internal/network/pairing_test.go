// internal/network/pairing_test.go
package network

import (
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const pairingTestKey = "PairingSecurityKey123"

func TestPreferOutbound(t *testing.T) {
	cases := []struct {
		local, remote     string
		prefer, decidable bool
	}{
		{"alpha", "bravo", true, true},
		{"bravo", "alpha", false, true},
		{"alpha", "alpha", false, false},
		// MachineName trims the space padding on the wire; a padded local name
		// must compare the same way or the two ends would disagree.
		{"alpha  ", "alpha", false, false},
		{"Linux", "linux", true, true}, // byte order, identical on both ends
	}
	for _, c := range cases {
		prefer, decidable := preferOutbound(c.local, c.remote)
		if prefer != c.prefer || decidable != c.decidable {
			t.Errorf("preferOutbound(%q, %q) = %v, %v; want %v, %v",
				c.local, c.remote, prefer, decidable, c.prefer, c.decidable)
		}
	}
	// Both ends must reach opposite conclusions about the same pair.
	a, _ := preferOutbound("desk", "laptop")
	b, _ := preferOutbound("laptop", "desk")
	if a == b {
		t.Fatal("both ends prefer the same direction; they would keep different sockets")
	}
}

func TestSelectLinuxPeerReturnsPreferredImmediately(t *testing.T) {
	out := make(chan *Conn, 1)
	out <- &Conn{LocalName: "alpha", RemoteName: "bravo"}
	start := time.Now()
	c, outbound := SelectLinuxPeer(out, nil, time.Hour)
	if c == nil || !outbound {
		t.Fatalf("got %v outbound=%v, want the outbound connection", c, outbound)
	}
	if time.Since(start) > time.Second {
		t.Fatal("waited for a grace period although the preferred connection arrived first")
	}
}

func TestSelectLinuxPeerFallsBackAfterGrace(t *testing.T) {
	// bravo prefers its inbound connection; only its outbound one exists, as
	// when alpha cannot reach bravo through a firewall.
	out := make(chan *Conn, 1)
	in := make(chan *Conn)
	want := &Conn{LocalName: "bravo", RemoteName: "alpha"}
	out <- want
	start := time.Now()
	c, outbound := SelectLinuxPeer(out, in, 50*time.Millisecond)
	if c != want || !outbound {
		t.Fatalf("got %v outbound=%v, want the only available connection", c, outbound)
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Fatal("did not give the preferred connection its grace period")
	}
}

func TestSelectLinuxPeerSameNameKeepsFirst(t *testing.T) {
	in := make(chan *Conn, 1)
	want := &Conn{LocalName: "linux", RemoteName: "linux"}
	in <- want
	c, outbound := SelectLinuxPeer(nil, in, time.Hour)
	if c != want || outbound {
		t.Fatalf("got %v outbound=%v, want the first connection", c, outbound)
	}
}

func TestSelectLinuxPeerClosedChannelsReturnNil(t *testing.T) {
	out, in := make(chan *Conn), make(chan *Conn)
	close(out)
	close(in)
	if c, _ := SelectLinuxPeer(out, in, time.Millisecond); c != nil {
		t.Fatal("expected nil when no connection source remains")
	}
}

// slowProxy forwards to target, delaying every chunk, to model one direction
// of the link being slower than the other (Wi-Fi on one end, say).
func slowProxy(t *testing.T, target string, d time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	pipe := func(dst, src net.Conn) {
		defer func() { _ = dst.Close() }()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				time.Sleep(d)
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	return ln.Addr().String()
}

type countingClipboard struct{ n atomic.Int32 }

func (c *countingClipboard) HandlePacket(*protocol.Packet) { c.n.Add(1) }

// TestLinuxPeersAgreeOnOneSocket runs two complete peers in-process, each
// listening and dialing the other exactly as cmd/mwb does, with one machine
// starting late and one direction slowed down. With first-wins selection this
// setup lands the two peers on different sockets in a few percent of runs,
// after which neither ever hears the other. Both must pick the same socket, and
// traffic must flow over it.
func TestLinuxPeersAgreeOnOneSocket(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-second pairing simulation")
	}
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 8; trial++ {
		portA, portB := freePort(t), freePort(t)
		stop := make(chan struct{})
		connectStop := make(chan struct{})

		inB, err := ListenAndAccept(portB, pairingTestKey, "bravo", "127.0.0.1", nil, nil, stop)
		if err != nil {
			t.Fatal(err)
		}
		toB := slowProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(portB)),
			time.Duration(80+rng.Intn(120))*time.Millisecond)
		outB := ConnectWithRetry(net.JoinHostPort("127.0.0.1", strconv.Itoa(portA)),
			pairingTestKey, "bravo", 5*time.Second, time.Second, connectStop)

		gotA, gotB := make(chan *Conn, 1), make(chan *Conn, 1)
		go func() {
			c, _ := SelectLinuxPeer(outB, inB, LinuxPeerGrace)
			gotB <- c
		}()
		stagger := time.Duration(rng.Intn(1500)) * time.Millisecond
		go func() {
			time.Sleep(stagger) // alpha boots after bravo
			inA, err := ListenAndAccept(portA, pairingTestKey, "alpha", "127.0.0.1", nil, nil, stop)
			if err != nil {
				gotA <- nil
				return
			}
			outA := ConnectWithRetry(toB, pairingTestKey, "alpha", 5*time.Second, time.Second, connectStop)
			c, _ := SelectLinuxPeer(outA, inA, LinuxPeerGrace)
			gotA <- c
		}()

		a, b := <-gotA, <-gotB
		close(connectStop)
		if a == nil || b == nil {
			t.Fatalf("trial %d: a peer got no connection", trial)
		}
		if a.MachineID != b.RemoteID || a.RemoteID != b.MachineID {
			t.Fatalf("trial %d (stagger %v): peers kept different sockets", trial, stagger)
		}

		// Prove the shared socket carries traffic end to end.
		clip := &countingClipboard{}
		done := make(chan error, 1)
		go func() { done <- ReceiveLoop(b, &Handler{Clipboard: clip}) }()
		ask := &protocol.Packet{Type: protocol.ClipboardAsk, Src: a.MachineID, Des: a.RemoteID}
		ask.SetMachineName("alpha")
		if err := a.SendPacket(ask); err != nil {
			t.Fatalf("trial %d: send: %v", trial, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for clip.n.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if clip.n.Load() == 0 {
			t.Fatalf("trial %d: packet sent by alpha never reached bravo", trial)
		}

		_ = a.Close()
		<-done
		_ = b.Close()
		close(stop)
	}
}

func TestIdleTimeoutDropsSilentPeer(t *testing.T) {
	a, b := connectedPair(t)
	defer func() { _ = b.Close() }()
	// b never reads or writes: from a's side the peer is attached to some
	// other socket, which is what a pairing mismatch looks like.
	a.SetIdleTimeout(300 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- ReceiveLoop(a, &Handler{}) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "silent") {
			t.Fatalf("ReceiveLoop returned %v, want a silent-peer error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("silent peer was never dropped")
	}
	_ = a.Close()
}

// The PowerToys path must keep its unbounded read: a quiet Windows peer is not
// an error there.
func TestNoIdleTimeoutByDefault(t *testing.T) {
	a, b := connectedPair(t)
	done := make(chan error, 1)
	go func() { done <- ReceiveLoop(a, &Handler{}) }()
	select {
	case err := <-done:
		t.Fatalf("ReceiveLoop returned %v without an idle timeout configured", err)
	case <-time.After(1500 * time.Millisecond):
	}
	_ = b.Close()
	_ = a.Close()
	<-done
}

// connectedPair returns both ends of one authenticated control connection.
func connectedPair(t *testing.T) (dialer, acceptor *Conn) {
	t.Helper()
	port := freePort(t)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	in, err := ListenAndAccept(port, pairingTestKey, "bravo", "127.0.0.1", nil, nil, stop)
	if err != nil {
		t.Fatal(err)
	}
	dialer, err = Connect(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), pairingTestKey, "alpha", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case acceptor = <-in:
	case <-time.After(10 * time.Second):
		t.Fatal("listener never delivered the inbound connection")
	}
	return dialer, acceptor
}
