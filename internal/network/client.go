// internal/network/client.go
package network

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const (
	handshakeTimeout      = 10 * time.Second
	peerResolutionTimeout = 3 * time.Second
	maxPendingHandshakes  = 4
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
	sendMu     sync.Mutex // guards enc.Write — CBC mode is not goroutine-safe
}

// Cached key material — PBKDF2 is expensive (50k iterations), only derive once.
var (
	keyMu        sync.Mutex
	cachedAESKey []byte
	cachedIV     []byte
	cachedMagic  uint32
	cachedSecret string
)

func getCachedKeyMaterial(securityKey string) ([]byte, []byte, uint32) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if securityKey == cachedSecret && cachedAESKey != nil {
		return cachedAESKey, cachedIV, cachedMagic
	}
	cachedAESKey = protocol.DeriveKey(securityKey)
	cachedIV = protocol.FixedIV()
	cachedMagic = protocol.Get24BitHash(securityKey)
	cachedSecret = securityKey
	return cachedAESKey, cachedIV, cachedMagic
}

// setupConn configures TCP options, creates crypto streams, exchanges IV, and
// performs a deadline-bounded handshake on an established TCP connection.
func setupConn(raw net.Conn, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set handshake deadline: %w", err)
	}

	aesKey, iv, magic := getCachedKeyMaterial(securityKey)

	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
		// Match OG MWB: 320KB send/recv buffers (default is ~16KB)
		_ = tc.SetWriteBuffer(320 * 1024)
		_ = tc.SetReadBuffer(320 * 1024)
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
	if err := raw.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear handshake deadline: %w", err)
	}

	return c, nil
}

// Connect establishes a TCP connection, performs IV exchange and handshake.
func Connect(addr, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	conn, err := setupConn(raw, securityKey, machineName, timeout)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}

// ConnectWithRetry keeps trying an outbound connection until one succeeds or
// stop is closed. The returned channel is intentionally unbuffered: if an
// inbound connection wins the race, closing stop guarantees that a concurrent
// outbound connection is closed instead of being left queued and leaked.
func ConnectWithRetry(
	addr, securityKey, machineName string,
	timeout, retryDelay time.Duration,
	stop <-chan struct{},
) <-chan *Conn {
	return connectWithRetry(stop, retryDelay, func() (*Conn, error) {
		return Connect(addr, securityKey, machineName, timeout)
	})
}

func connectWithRetry(
	stop <-chan struct{},
	retryDelay time.Duration,
	connect func() (*Conn, error),
) <-chan *Conn {
	connCh := make(chan *Conn)

	go func() {
		defer close(connCh)

		for {
			conn, err := connect()
			if err == nil {
				select {
				case connCh <- conn:
				case <-stop:
					_ = conn.Close()
				}
				return
			}

			slog.Debug("outbound connect failed; retrying", "err", err, "delay", retryDelay)
			timer := time.NewTimer(retryDelay)
			select {
			case <-timer.C:
			case <-stop:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
		}
	}()

	return connCh
}

// resolveAllowedPeer resolves the configured host once when the listener
// starts. Keeping DNS out of the accept loop prevents untrusted connection
// attempts from triggering repeated blocking lookups.
func resolveAllowedPeer(allowedHost string) ([]net.IP, error) {
	if allowedHost == "" {
		return nil, fmt.Errorf("configured host is empty")
	}
	if ip := net.ParseIP(allowedHost); ip != nil {
		return []net.IP{ip}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), peerResolutionTimeout)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", allowedHost)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses found")
	}
	return ips, nil
}

// isAllowedPeer reports whether addr belongs to the peer resolved from the
// configured host. An empty allowlist rejects every connection (fail closed).
func isAllowedPeer(addr net.Addr, allowedIPs []net.IP) bool {
	var remoteIP net.IP
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		remoteIP = tcpAddr.IP
	} else {
		host, _, err := net.SplitHostPort(addr.String())
		if err != nil {
			return false
		}
		remoteIP = net.ParseIP(host)
	}
	if remoteIP == nil {
		return false
	}
	for _, allowedIP := range allowedIPs {
		if allowedIP.Equal(remoteIP) {
			return true
		}
	}
	return false
}

// ListenAndAccept starts a TCP server on the given port and sends accepted
// connections (after handshake) to the returned channel. This allows Windows
// MWB to connect TO us, which is faster after lock/reconnect cycles.
//
// allowedHost restricts inbound connections to the configured peer. Hostnames
// are resolved once at listener startup; resolution failure disables inbound
// connections while leaving the outbound retry path available.
func ListenAndAccept(port int, securityKey, machineName, allowedHost string, stop chan struct{}) (chan *Conn, error) {
	connCh := make(chan *Conn, 1)
	allowedIPs, resolveErr := resolveAllowedPeer(allowedHost)
	if resolveErr != nil {
		slog.Warn("could not resolve configured inbound peer; inbound connections disabled",
			"host", allowedHost, "err", resolveErr)
	}

	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	go func() {
		defer func() { _ = ln.Close() }()
		var handshakes sync.WaitGroup
		defer func() {
			handshakes.Wait()
			close(connCh)
		}()
		pending := make(chan struct{}, maxPendingHandshakes)

		slog.Info("listening for incoming MWB connections",
			"port", port, "allowedHost", allowedHost, "allowedIPs", allowedIPs)

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

			// Reject any source that is not the configured peer before it can
			// consume handshake work. The shared key remains the authentication
			// control; the IP allowlist is defense in depth.
			if !isAllowedPeer(raw.RemoteAddr(), allowedIPs) {
				slog.Warn("rejected inbound connection from unexpected source",
					"remote", raw.RemoteAddr(), "allowedHost", allowedHost)
				_ = raw.Close()
				continue
			}

			select {
			case pending <- struct{}{}:
			default:
				slog.Warn("rejected inbound connection: too many pending handshakes",
					"remote", raw.RemoteAddr())
				_ = raw.Close()
				continue
			}

			handshakes.Add(1)
			go func(raw net.Conn) {
				defer handshakes.Done()
				defer func() { <-pending }()

				slog.Info("incoming connection", "remote", raw.RemoteAddr())
				conn, err := setupConn(raw, securityKey, machineName, handshakeTimeout)
				if err != nil {
					slog.Error("incoming handshake failed", "remote", raw.RemoteAddr(), "err", err)
					_ = raw.Close()
					return
				}

				slog.Info("incoming connection established", "remote", conn.RemoteName)
				select {
				case connCh <- conn:
				case <-stop:
					_ = conn.Close()
				}
			}(raw)
		}
	}()

	return connCh, nil
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

	// Read packets until we get a valid HandshakeAck. setupConn applies the
	// deadline for the full IV exchange and handshake, including writes.
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

// SendPacket marshals, stamps, and sends a packet with a 500ms write deadline
// matching OG MWB's SendTimeout to prevent stuck writes from blocking.
// sendMu serializes all writes: cipher.CBCEncrypter mutates internal IV state
// on every call and is not safe for concurrent use.
func (c *Conn) SendPacket(p *protocol.Packet) error {
	// Wrap before reaching 0x7FFFFFFF — protocol requires non-zero, non-negative IDs.
	if c.nextID.Load() >= 0x7FFFFF00 {
		c.nextID.Store(0)
	}
	p.ID = c.nextID.Add(1)
	buf := p.Marshal()
	protocol.StampPacket(buf, c.magic)
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
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

// Close sends a ByeBye packet (like OG MWB) then closes the connection.
func (c *Conn) Close() error {
	bye := &protocol.Packet{
		Type: protocol.ByeBye,
		Src:  c.MachineID,
		Des:  protocol.IDAll,
	}
	_ = c.SendPacket(bye) // best-effort, don't block on failure
	return c.raw.Close()
}
