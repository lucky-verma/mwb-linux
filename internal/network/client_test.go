// internal/network/client_test.go
package network

import (
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

func TestConnectionHandshake(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	aesKey := protocol.DeriveKey(securityKey)
	iv := protocol.FixedIV()
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

		// Server: create encrypted streams
		enc, err := protocol.NewEncryptWriter(conn, aesKey, iv)
		if err != nil {
			serverDone <- err
			return
		}
		dec, err := protocol.NewDecryptReader(conn, aesKey, iv)
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

		// Server: read random IV block from client
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
