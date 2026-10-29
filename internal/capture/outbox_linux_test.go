//go:build linux

package capture

import (
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

func movePkt(x int32) *protocol.Packet {
	p := &protocol.Packet{Type: protocol.Mouse}
	p.Mouse.X, p.Mouse.DwFlags = x, protocol.WM_MOUSEMOVE
	return p
}

func clickPkt(flags int32) *protocol.Packet {
	p := &protocol.Packet{Type: protocol.Mouse}
	p.Mouse.DwFlags = flags
	return p
}

// Moves waiting to be sent collapse into the newest; other input keeps its
// order and is never dropped, and a move made before a click goes first.
func TestOutboxKeepsNewestMoveAndOrdersTheRest(t *testing.T) {
	o := newOutbox()
	var sent []*protocol.Packet
	send := func(p *protocol.Packet) error { sent = append(sent, p); return nil }

	o.put(movePkt(1), nil, send, true)
	o.put(movePkt(2), nil, send, true) // replaces 1
	o.put(clickPkt(protocol.WM_LBUTTONDOWN), nil, send, false)
	o.put(clickPkt(protocol.WM_LBUTTONUP), nil, send, false)
	o.put(movePkt(3), nil, send, true)

	for {
		it, _ := o.next()
		if it == nil {
			break
		}
		_ = it.send(it.pkt)
	}
	want := []int32{protocol.WM_MOUSEMOVE, protocol.WM_LBUTTONDOWN, protocol.WM_LBUTTONUP, protocol.WM_MOUSEMOVE}
	// 1 was replaced by 2 before the click; 3 came after it.
	if len(sent) != len(want) {
		t.Fatalf("sent %d packets, want %d", len(sent), len(want))
	}
	for i, w := range want {
		if sent[i].Mouse.DwFlags != w {
			t.Fatalf("packet %d is %#x, want %#x", i, sent[i].Mouse.DwFlags, w)
		}
	}
	if sent[0].Mouse.X != 2 || sent[3].Mouse.X != 3 {
		t.Fatalf("moves sent %d and %d, want 2 and 3", sent[0].Mouse.X, sent[3].Mouse.X)
	}
}

// The running outbox delivers what it is given and stops with stop.
func TestOutboxRunDelivers(t *testing.T) {
	o := newOutbox()
	got := make(chan int32, 4)
	send := func(p *protocol.Packet) error { got <- p.Mouse.DwFlags; return nil }
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { o.run(stop); close(done) }()
	o.put(clickPkt(protocol.WM_LBUTTONDOWN), nil, send, false)
	select {
	case f := <-got:
		if f != protocol.WM_LBUTTONDOWN {
			t.Fatalf("got %#x", f)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing delivered")
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not stop")
	}
}
