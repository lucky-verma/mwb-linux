// internal/network/client.go
package network

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"time"

	"github.com/bketelsen/mwb/internal/protocol"
)

// Conn represents an established, encrypted MWB connection.
type Conn struct {
	raw        net.Conn
	enc        *protocol.EncryptWriter
	dec        *protocol.DecryptReader
	magic      uint32
	MachineID  uint32
	RemoteID   uint32
	LocalName  string
	RemoteName string
	nextID     atomic.Int32
}

// Cached key material — PBKDF2 is expensive (50k iterations), only derive once.
var (
	cachedAESKey []byte
	cachedIV     []byte
	cachedMagic  uint32
	cachedSecret string
)

func getCachedKeyMaterial(securityKey string) ([]byte, []byte, uint32) {
	if securityKey == cachedSecret && cachedAESKey != nil {
		return cachedAESKey, cachedIV, cachedMagic
	}
	cachedAESKey = protocol.DeriveKey(securityKey)
	cachedIV = protocol.FixedIV()
	cachedMagic = protocol.Get24BitHash(securityKey)
	cachedSecret = securityKey
	return cachedAESKey, cachedIV, cachedMagic
}

// setupConn configures TCP options, creates crypto streams, exchanges IV,
// and performs handshake on an already-established TCP connection.
func setupConn(raw net.Conn, securityKey, machineName string) (*Conn, error) {
	aesKey, iv, magic := getCachedKeyMaterial(securityKey)

	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
	}

	enc, err := protocol.NewEncryptWriter(raw, aesKey, iv)
	if err != nil {
		return nil, fmt.Errorf("encrypt stream: %w", err)
	}
	dec, err := protocol.NewDecryptReader(raw, aesKey, iv)
	if err != nil {
		return nil, fmt.Errorf("decrypt stream: %w", err)
	}

	// IV exchange: send random 16-byte block, read peer's random block
	ranData := make([]byte, 16)
	if _, err := rand.Read(ranData); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	if _, err := enc.Write(ranData); err != nil {
		return nil, fmt.Errorf("send IV block: %w", err)
	}
	peerRan := make([]byte, 16)
	if _, err := io.ReadFull(dec, peerRan); err != nil {
		return nil, fmt.Errorf("read IV block: %w", err)
	}

	// Generate a machine ID from random data
	machineID := uint32(ranData[0])<<24 | uint32(ranData[1])<<16 | uint32(ranData[2])<<8 | uint32(ranData[3])
	if machineID == 0 || machineID == 255 {
		machineID = 1
	}

	c := &Conn{
		raw:       raw,
		enc:       enc,
		dec:       dec,
		magic:     magic,
		MachineID: machineID,
		LocalName: machineName,
	}

	if err := c.doHandshake(machineName); err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}

	// Send initial heartbeat to trigger device registration on the server
	hb := &protocol.Packet{
		Type: protocol.HeartbeatEx,
		Src:  c.MachineID,
		Des:  protocol.IDAll,
	}
	hb.SetMachineName(machineName)
	if err := c.SendPacket(hb); err != nil {
		return nil, fmt.Errorf("send heartbeat: %w", err)
	}

	return c, nil
}

// Connect establishes a TCP connection, performs IV exchange and handshake.
func Connect(addr, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	conn, err := setupConn(raw, securityKey, machineName)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

// ListenAndAccept starts a TCP server on the given port and sends accepted
// connections (after handshake) to the returned channel. This allows Windows
// MWB to connect TO us, which is faster after lock/reconnect cycles.
func ListenAndAccept(port int, securityKey, machineName string, stop chan struct{}) chan *Conn {
	connCh := make(chan *Conn, 1)

	go func() {
		defer close(connCh)
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			slog.Error("server listen failed", "addr", addr, "err", err)
			return
		}
		defer ln.Close()
		slog.Info("listening for incoming MWB connections", "port", port)

		for {
			select {
			case <-stop:
				return
			default:
			}

			// Set accept deadline so we can check stop channel periodically
			if tl, ok := ln.(*net.TCPListener); ok {
				_ = tl.SetDeadline(time.Now().Add(1 * time.Second))
			}

			raw, err := ln.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				slog.Debug("accept error", "err", err)
				continue
			}

			slog.Info("incoming connection", "remote", raw.RemoteAddr())
			conn, err := setupConn(raw, securityKey, machineName)
			if err != nil {
				slog.Error("incoming handshake failed", "err", err)
				_ = raw.Close()
				continue
			}

			slog.Info("incoming connection established", "remote", conn.RemoteName)
			select {
			case connCh <- conn:
			case <-stop:
				_ = conn.Close()
				return
			}
		}
	}()

	return connCh
}

func (c *Conn) doHandshake(machineName string) error {
	hs := &protocol.Packet{
		Type: protocol.Handshake,
		ID:   1,
		Src:  c.MachineID,
		Des:  0,
	}

	// Random machine fields for challenge
	challenge := make([]byte, 16)
	_, _ = rand.Read(challenge)
	hs.Handshake.Machine1 = uint32(challenge[0])<<24 | uint32(challenge[1])<<16 | uint32(challenge[2])<<8 | uint32(challenge[3])
	hs.Handshake.Machine2 = uint32(challenge[4])<<24 | uint32(challenge[5])<<16 | uint32(challenge[6])<<8 | uint32(challenge[7])
	hs.Handshake.Machine3 = uint32(challenge[8])<<24 | uint32(challenge[9])<<16 | uint32(challenge[10])<<8 | uint32(challenge[11])
	hs.Handshake.Machine4 = uint32(challenge[12])<<24 | uint32(challenge[13])<<16 | uint32(challenge[14])<<8 | uint32(challenge[15])
	hs.SetMachineName(machineName)

	// Expected response: bitwise inverted fields
	expect1 := ^hs.Handshake.Machine1
	expect2 := ^hs.Handshake.Machine2
	expect3 := ^hs.Handshake.Machine3
	expect4 := ^hs.Handshake.Machine4

	// Send 10 handshake packets (per MWB protocol)
	for i := 0; i < 10; i++ {
		if err := c.SendPacket(hs); err != nil {
			return fmt.Errorf("send handshake %d: %w", i, err)
		}
	}

	// Read packets until we get a valid HandshakeAck
	_ = c.raw.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = c.raw.SetReadDeadline(time.Time{}) }()

	for i := 0; i < 20; i++ {
		pkt, err := c.RecvPacket()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		if pkt.Type == protocol.Handshake {
			// Peer's handshake; respond with ACK
			ack := &protocol.Packet{
				Type: protocol.HandshakeAck,
				Src:  c.MachineID,
				Des:  pkt.Src,
			}
			ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
			ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
			ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
			ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
			ack.SetMachineName(machineName)
			if err := c.SendPacket(ack); err != nil {
				return fmt.Errorf("send handshake ack: %w", err)
			}
			continue
		}

		if pkt.Type == protocol.HandshakeAck {
			if pkt.Handshake.Machine1 == expect1 &&
				pkt.Handshake.Machine2 == expect2 &&
				pkt.Handshake.Machine3 == expect3 &&
				pkt.Handshake.Machine4 == expect4 {
				c.RemoteName = pkt.MachineName()
				c.RemoteID = pkt.Src
				slog.Info("handshake complete", "remote", c.RemoteName, "remoteID", c.RemoteID)
				return nil
			}
			return fmt.Errorf("handshake verification failed")
		}
	}

	return fmt.Errorf("no HandshakeAck received")
}

// SendPacket marshals, stamps, and sends a packet.
func (c *Conn) SendPacket(p *protocol.Packet) error {
	p.ID = c.nextID.Add(1)
	buf := p.Marshal()
	protocol.StampPacket(buf, c.magic)
	_, err := c.enc.Write(buf)
	return err
}

// RecvPacket reads, validates, and unmarshals a packet.
func (c *Conn) RecvPacket() (*protocol.Packet, error) {
	buf := make([]byte, protocol.PacketSize)
	if _, err := io.ReadFull(c.dec, buf); err != nil {
		return nil, fmt.Errorf("read packet: %w", err)
	}

	if err := protocol.ValidatePacket(buf, c.magic); err != nil {
		return nil, err
	}
	protocol.ClearStamp(buf)

	pt := protocol.PackageType(buf[0])
	if protocol.IsBigPacket(pt) {
		ext := make([]byte, protocol.PacketSize)
		if _, err := io.ReadFull(c.dec, ext); err != nil {
			return nil, fmt.Errorf("read extended: %w", err)
		}
		full := make([]byte, protocol.PacketSizeEx)
		copy(full, buf)
		copy(full[protocol.PacketSize:], ext)
		return protocol.UnmarshalPacket(full)
	}

	return protocol.UnmarshalPacket(buf)
}

// Close closes the connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}
