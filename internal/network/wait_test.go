// internal/network/wait_test.go
package network

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// TestWaitingLinuxPeer runs the Wait/Connect pairing in-process: one machine
// has no host and only accepts, the other dials it, exactly as cmd/mwb wires
// them. Both name orders matter, because the pairing rule prefers one
// direction and the waiting side never dials.
func TestWaitingLinuxPeer(t *testing.T) {
	// Two peers on loopback stand in for two machines on a LAN.
	saved := localNetworkSource
	localNetworkSource = func(ip net.IP) bool { return ip.IsLoopback() || isLocalNetwork(ip) }
	t.Cleanup(func() { localNetworkSource = saved })

	for _, names := range [][2]string{{"alpha", "bravo"}, {"bravo", "alpha"}} {
		waiterName, dialerName := names[0], names[1]
		t.Run("waiter="+waiterName, func(t *testing.T) {
			waitPort, dialPort := freePort(t), freePort(t)
			stop := make(chan struct{})
			defer close(stop)
			connectStop := make(chan struct{})
			defer close(connectStop)

			// The waiting machine: no host, no outbound dial.
			inW, err := ListenAndAccept(waitPort, pairingTestKey, waiterName, AcceptLocalNetwork, nil, nil, stop)
			if err != nil {
				t.Fatal(err)
			}
			// The dialing machine: host set to the waiter, and listening too.
			inD, err := ListenAndAccept(dialPort, pairingTestKey, dialerName, "127.0.0.1", nil, nil, stop)
			if err != nil {
				t.Fatal(err)
			}
			outD := ConnectWithRetry(net.JoinHostPort("127.0.0.1", strconv.Itoa(waitPort)),
				pairingTestKey, dialerName, 5*time.Second, 200*time.Millisecond, connectStop)

			const grace = 500 * time.Millisecond
			gotW, gotD := make(chan *Conn, 1), make(chan *Conn, 1)
			go func() { c, _ := SelectLinuxPeer(nil, inW, grace); gotW <- c }()
			go func() { c, _ := SelectLinuxPeer(outD, inD, grace); gotD <- c }()

			var w, d *Conn
			for w == nil || d == nil {
				select {
				case w = <-gotW:
				case d = <-gotD:
				case <-time.After(15 * time.Second):
					t.Fatal("waiting pair never connected")
				}
			}
			if w.MachineID != d.RemoteID || d.MachineID != w.RemoteID {
				t.Fatal("the two machines kept different sockets")
			}
			if w.RemoteName != dialerName || d.RemoteName != waiterName {
				t.Fatalf("names: waiter sees %q, dialer sees %q", w.RemoteName, d.RemoteName)
			}
			// Where the waiter sends files: the address the dialer came from.
			if h := w.RemoteHost(); net.ParseIP(h) == nil || !net.ParseIP(h).IsLoopback() {
				t.Fatalf("waiter learned peer host %q", h)
			}

			// Traffic flows from the waiter to the dialer.
			clip := &countingClipboard{}
			done := make(chan error, 1)
			go func() { done <- ReceiveLoop(d, &Handler{Clipboard: clip}) }()
			ask := &protocol.Packet{Type: protocol.ClipboardAsk, Src: w.MachineID, Des: w.RemoteID}
			ask.SetMachineName(waiterName)
			if err := w.SendPacket(ask); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for clip.n.Load() == 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if clip.n.Load() == 0 {
				t.Fatal("packet from the waiting machine never arrived")
			}
			_ = w.Close()
			<-done
			_ = d.Close()
		})
	}
}

// Without the test override, a waiting machine refuses loopback, like any
// other non-LAN source.
func TestWaitingPeerRefusesLoopbackInProduction(t *testing.T) {
	allow, _, _ := peerFilter(AcceptLocalNetwork)
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:1")
	if allow(addr) {
		t.Fatal("loopback accepted")
	}
}

// Both peers send their PeerInfo over a real connection and each receives
// the other's, as cmd/mwb's exchangePeerInfo does right after connecting.
func TestPeerInfoExchangeOverConnection(t *testing.T) {
	a, b := connectedPair(t)
	gotA, gotB := make(chan protocol.PeerInfo, 1), make(chan protocol.PeerInfo, 1)
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- ReceiveLoop(a, &Handler{OnPeerInfo: func(i protocol.PeerInfo) { gotA <- i }}) }()
	go func() { doneB <- ReceiveLoop(b, &Handler{OnPeerInfo: func(i protocol.PeerInfo) { gotB <- i }}) }()

	infoA := protocol.PeerInfo{ScreenWidth: 2560, ScreenHeight: 1440, Edge: "right", Sharing: true}
	infoB := protocol.PeerInfo{ScreenWidth: 1920, ScreenHeight: 1200, Edge: "left", Sharing: false}
	if err := a.SendPacket(protocol.NewPeerInfoPacket(a.MachineID, a.RemoteID, infoA)); err != nil {
		t.Fatal(err)
	}
	if err := b.SendPacket(protocol.NewPeerInfoPacket(b.MachineID, b.RemoteID, infoB)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ch   chan protocol.PeerInfo
		want protocol.PeerInfo
		who  string
	}{{gotB, infoA, "bravo"}, {gotA, infoB, "alpha"}} {
		select {
		case got := <-c.ch:
			if got != c.want {
				t.Fatalf("%s received %+v, want %+v", c.who, got, c.want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s never received the peer's settings", c.who)
		}
	}
	_ = a.Close()
	<-doneB
	_ = b.Close()
	<-doneA
}
