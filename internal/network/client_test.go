// internal/network/client_test.go
package network

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

func TestConnectionHandshake(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	magic := protocol.Get24BitHash(securityKey)

	// Start a fake MWB server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer func() { _ = conn.Close() }()

		// Server: create encrypted streams. Send side first, so that neither
		// end is left waiting for the other's salt+IV header.
		enc, err := protocol.NewEncryptWriterWithHeader(conn, securityKey)
		if err != nil {
			serverDone <- err
			return
		}

		// Server: send random IV block
		ranData := make([]byte, 16)
		_, _ = rand.Read(ranData)
		if _, err := enc.Write(ranData); err != nil {
			serverDone <- err
			return
		}

		// Server: read the client's header, then its random IV block
		dec, err := protocol.NewDecryptReaderWithHeader(conn, securityKey)
		if err != nil {
			serverDone <- err
			return
		}
		clientRan := make([]byte, 16)
		if _, err := io.ReadFull(dec, clientRan); err != nil {
			serverDone <- err
			return
		}

		// Server: read handshake packets from client (may receive up to 10)
		// Read at least one handshake packet (64 bytes for big packet)
		pktBuf := make([]byte, protocol.PacketSizeEx)
		if _, err := io.ReadFull(dec, pktBuf); err != nil {
			serverDone <- err
			return
		}

		if err := protocol.ValidatePacket(pktBuf, magic); err != nil {
			serverDone <- err
			return
		}
		protocol.ClearStamp(pktBuf)

		pkt, err := protocol.UnmarshalPacket(pktBuf)
		if err != nil {
			serverDone <- err
			return
		}

		if pkt.Type != protocol.Handshake {
			serverDone <- fmt.Errorf("expected Handshake, got %d", pkt.Type)
			return
		}

		// Drain remaining handshake packets (client sends 10)
		for i := 0; i < 9; i++ {
			drain := make([]byte, protocol.PacketSizeEx)
			_, _ = io.ReadFull(dec, drain)
		}

		// Server: send HandshakeAck with inverted machine fields
		ack := &protocol.Packet{
			Type: protocol.HandshakeAck,
			Src:  0,
			Des:  pkt.Src,
		}
		ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
		ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
		ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
		ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
		ack.SetMachineName("WINHOST")

		ackBuf := ack.Marshal()
		protocol.StampPacket(ackBuf, magic)
		if _, err := enc.Write(ackBuf); err != nil {
			serverDone <- err
			return
		}

		serverDone <- nil
	}()

	// Client: connect and handshake
	addr := ln.Addr().String()
	conn, err := Connect(addr, securityKey, "linux-test", 5*time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if conn.RemoteName != "WINHOST" {
		t.Errorf("remote name = %q, want %q", conn.RemoteName, "WINHOST")
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for server")
	}
}

func TestListenAndAcceptReturnsBindError(t *testing.T) {
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	port := ln.Addr().(*net.TCPAddr).Port
	stop := make(chan struct{})
	connCh, err := ListenAndAccept(port, "TestSecurityKey!!", "linux-test", "192.168.1.100", nil, stop)
	if err == nil {
		close(stop)
		t.Fatal("expected bind error, got nil")
	}
	if connCh != nil {
		t.Fatalf("expected nil connection channel on bind error, got %v", connCh)
	}
}

func TestConnectWithRetryRetriesUntilSuccess(t *testing.T) {
	stop := make(chan struct{})
	want := &Conn{}
	var attempts atomic.Int32

	connCh := connectWithRetry(stop, time.Millisecond, func() (*Conn, error) {
		if attempts.Add(1) < 3 {
			return nil, errors.New("server unavailable")
		}
		return want, nil
	})

	select {
	case got := <-connCh:
		if got != want {
			t.Fatalf("connection = %p, want %p", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for retried connection")
	}

	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestConnectWithRetryStopsDuringDelay(t *testing.T) {
	stop := make(chan struct{})
	attempted := make(chan struct{}, 1)

	connCh := connectWithRetry(stop, time.Hour, func() (*Conn, error) {
		attempted <- struct{}{}
		return nil, errors.New("server unavailable")
	})

	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("initial connection attempt did not run")
	}

	close(stop)
	select {
	case conn, ok := <-connCh:
		if ok {
			t.Fatalf("unexpected connection after stop: %v", conn)
		}
	case <-time.After(time.Second):
		t.Fatal("retry loop did not stop")
	}
}

// TestSecureStreamLegacyMode covers the escape hatch for a Windows peer pinned
// to PowerToys older than 0.101: with legacy_crypto set, neither side sends a
// salt+IV header and both derive the fixed-salt key instead.
func TestSecureStreamLegacyMode(t *testing.T) {
	SetLegacyCrypto(true)
	t.Cleanup(func() { SetLegacyCrypto(false) })

	const securityKey = "TestSecurityKey!!"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	peerDone := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer func() { _ = raw.Close() }()
		_, err = newSecureStream(raw, securityKey, "windows", 5*time.Second)
		peerDone <- err
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	if _, err := newSecureStream(raw, securityKey, "linux", 5*time.Second); err != nil {
		t.Fatalf("client secure stream in legacy mode: %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer secure stream in legacy mode: %v", err)
	}
}

// TestSecureStreamCurrentMode is the same exchange in the default scheme, where
// both ends do send a cleartext salt+IV header.
func TestSecureStreamCurrentMode(t *testing.T) {
	const securityKey = "TestSecurityKey!!"

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	peerDone := make(chan error, 1)
	go func() {
		raw, err := ln.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		defer func() { _ = raw.Close() }()
		_, err = newSecureStream(raw, securityKey, "windows", 5*time.Second)
		peerDone <- err
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()

	if _, err := newSecureStream(raw, securityKey, "linux", 5*time.Second); err != nil {
		t.Fatalf("client secure stream: %v", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer secure stream: %v", err)
	}
}
