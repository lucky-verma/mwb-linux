package network

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// A heartbeat to everyone gets one answer; an answer gets none, so two mwb
// peers cannot echo one heartbeat between them forever.
func TestHeartbeatAnswerIsNotAnswered(t *testing.T) {
	a, b := connectedPair(t)
	defer func() { _ = a.Close(); _ = b.Close() }()
	done := make(chan error, 1)
	go func() { done <- ReceiveLoop(a, &Handler{}) }()

	send := func(des uint32) {
		t.Helper()
		hb := &protocol.Packet{Type: protocol.HeartbeatEx, Src: b.MachineID, Des: des}
		if err := b.SendPacket(hb); err != nil {
			t.Fatal(err)
		}
	}
	// nextAnswer skips the rest of the handshake and a's own heartbeats,
	// which go to everyone.
	nextAnswer := func(wait time.Duration) (*protocol.Packet, error) {
		_ = b.raw.SetReadDeadline(time.Now().Add(wait))
		for {
			p, err := b.RecvPacket()
			if err != nil || (p.Type == protocol.HeartbeatEx && p.Des != protocol.IDAll) {
				return p, err
			}
		}
	}

	send(protocol.IDAll)
	resp, err := nextAnswer(2 * time.Second)
	if err != nil {
		t.Fatalf("no answer to a heartbeat: %v", err)
	}
	// The heartbeat b sent while connecting is answered too.
	for err == nil {
		_, err = nextAnswer(300 * time.Millisecond)
	}

	send(resp.Src) // echo the answer back, as an older mwb does
	var ne net.Error
	if _, err := nextAnswer(300 * time.Millisecond); err == nil {
		t.Fatal("an answer was answered")
	} else if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("read: %v", err)
	}
}

// On a link with an idle timeout, heartbeats keep two quiet but healthy
// peers connected past the timeout, and a peer that stops answering is
// dropped soon after it.
func TestIdleTimeoutKeepsHealthyLinksAndDropsSilentOnes(t *testing.T) {
	const timeout = 500 * time.Millisecond
	a, b := connectedPair(t)
	defer func() { _ = a.Close(); _ = b.Close() }()
	a.SetIdleTimeout(timeout)
	b.SetIdleTimeout(timeout)
	doneA, doneB := make(chan error, 1), make(chan error, 1)
	go func() { doneA <- ReceiveLoop(a, &Handler{}) }()
	stopB := make(chan struct{})
	go func() {
		// b answers until told to go silent, then neither reads nor writes.
		done := make(chan error, 1)
		go func() { done <- ReceiveLoop(b, &Handler{}) }()
		select {
		case err := <-done:
			doneB <- err
		case <-stopB:
			doneB <- nil
		}
	}()

	select {
	case err := <-doneA:
		t.Fatalf("a healthy link ended: %v", err)
	case err := <-doneB:
		t.Fatalf("a healthy link ended: %v", err)
	case <-time.After(3 * timeout):
	}

	// b goes silent: its loop keeps running only if it still gets packets,
	// so stop its writes by holding its send lock.
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	close(stopB)
	start := time.Now()
	select {
	case err := <-doneA:
		if err == nil {
			t.Fatal("a silent peer ended the link without an error")
		}
		if waited := time.Since(start); waited > 2*timeout {
			t.Fatalf("a silent peer was noticed after %s, want about %s", waited, timeout)
		}
	case <-time.After(4 * timeout):
		t.Fatal("a silent peer was never dropped")
	}
}
