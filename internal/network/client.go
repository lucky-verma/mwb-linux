// internal/network/client.go
package network

import (
	"context"
	"crypto/rand"
	"encoding/binary"
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
	keyMu    sync.Mutex
	keyCache struct {
		secret  string
		magic   uint32
		magicOK bool
		legacy  []byte
	}
)

// legacyCrypto selects the stream encryption PowerToys used before 0.101. It is
// set once at startup from config, ahead of any connection attempt.
var legacyCrypto atomic.Bool

// SetLegacyCrypto selects the pre-0.101 PowerToys stream encryption: a fixed
// PBKDF2 salt and IV at 50,000 iterations, rather than the per-connection
// random salt and IV that PowerToys has sent in the clear since 0.101. Call it
// before opening any connection; it is not meant to change at runtime.
func SetLegacyCrypto(on bool) {
	legacyCrypto.Store(on)
}

// resetKeyCacheLocked drops cached material when the security key changes.
// Callers must hold keyMu.
func resetKeyCacheLocked(securityKey string) {
	if keyCache.secret != securityKey {
		keyCache = struct {
			secret  string
			magic   uint32
			magicOK bool
			legacy  []byte
		}{secret: securityKey}
	}
}

// getCachedMagic caches the security-key magic value, which costs 50k SHA-512
// rounds. The stream key is not cached alongside it: outside legacy mode
// PowerToys picks a fresh PBKDF2 salt per stream, so that key is derived per
// connection.
func getCachedMagic(securityKey string) uint32 {
	keyMu.Lock()
	defer keyMu.Unlock()
	resetKeyCacheLocked(securityKey)
	if !keyCache.magicOK {
		keyCache.magic = protocol.Get24BitHash(securityKey)
		keyCache.magicOK = true
	}
	return keyCache.magic
}

// getCachedLegacyKey caches the pre-0.101 fixed-salt key. It is derived lazily,
// so a peer on current PowerToys never pays for it.
func getCachedLegacyKey(securityKey string) []byte {
	keyMu.Lock()
	defer keyMu.Unlock()
	resetKeyCacheLocked(securityKey)
	if keyCache.legacy == nil {
		keyCache.legacy = protocol.DeriveKey(securityKey)
	}
	return keyCache.legacy
}

// newEncryptWriter opens the send half of the stream in whichever scheme the
// Windows peer speaks. The current scheme writes a cleartext salt+IV header
// first; the legacy one starts straight into ciphertext.
func newEncryptWriter(w io.Writer, securityKey string) (*protocol.EncryptWriter, error) {
	if legacyCrypto.Load() {
		return protocol.NewEncryptWriter(w, getCachedLegacyKey(securityKey), protocol.FixedIV())
	}
	return protocol.NewEncryptWriterWithHeader(w, securityKey)
}

// newDecryptReader opens the receive half. In the current scheme this blocks
// until the peer has sent its header, which is why callers must flush their own
// send side first.
func newDecryptReader(r io.Reader, securityKey string) (*protocol.DecryptReader, error) {
	if legacyCrypto.Load() {
		return protocol.NewDecryptReader(r, getCachedLegacyKey(securityKey), protocol.FixedIV())
	}
	return protocol.NewDecryptReaderWithHeader(r, securityKey)
}

// newSecureStream configures TCP options, creates the AES-CBC streams and
// performs the IV block exchange. It stops before the handshake, because the
// file-transfer channel shares this encrypted prefix but continues differently
// on the adjacent clipboard port: it sends a raw Clipboard/ClipboardPush DATA
// struct where the control channel sends a stamped Handshake packet.
func newSecureStream(raw net.Conn, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	if timeout <= 0 {
		timeout = handshakeTimeout
	}
	if err := raw.SetDeadline(time.Now().Add(timeout)); err != nil {
		return nil, fmt.Errorf("set handshake deadline: %w", err)
	}

	magic := getCachedMagic(securityKey)

	if tc, ok := raw.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(10 * time.Second)
		// Match OG MWB: 320KB send/recv buffers (default is ~16KB)
		_ = tc.SetWriteBuffer(320 * 1024)
		_ = tc.SetReadBuffer(320 * 1024)
	}

	// Send side first, receive side second. PowerToys builds its encrypted and
	// decrypted streams lazily, so it may well read before it writes: if we
	// blocked on its header before flushing our own, both ends would sit
	// waiting for the other to speak first. The order is harmless in legacy
	// mode, where neither side sends a header at all.
	enc, err := newEncryptWriter(raw, securityKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt stream: %w", err)
	}

	// IV exchange: send random 16-byte block, read peer's random block
	ranData := make([]byte, 16)
	if _, err := rand.Read(ranData); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	if _, err := enc.Write(ranData); err != nil {
		return nil, fmt.Errorf("send IV block: %w", err)
	}

	dec, err := newDecryptReader(raw, securityKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt stream: %w", err)
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

	return c, nil
}

// setupConn establishes the control connection: secure stream, then handshake.
// Outbound connections always take this path.
func setupConn(raw net.Conn, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	c, err := newSecureStream(raw, securityKey, machineName, timeout)
	if err != nil {
		return nil, err
	}
	if err := c.finishControlSetup(machineName, nil); err != nil {
		return nil, err
	}
	return c, nil
}

// finishControlSetup completes a control connection once the secure stream
// exists. first is a packet already read from the stream, or nil.
func (c *Conn) finishControlSetup(machineName string, first *protocol.Packet) error {
	if err := c.doHandshake(machineName, first); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	// Send initial heartbeat to trigger device registration on the server
	hb := &protocol.Packet{
		Type: protocol.HeartbeatEx,
		Src:  c.MachineID,
		Des:  protocol.IDAll,
	}
	hb.SetMachineName(machineName)
	if err := c.SendPacket(hb); err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	if err := c.raw.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear handshake deadline: %w", err)
	}

	return nil
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

// isAllowedPeer reports whether addr may attempt the handshake.
//
// Matching the configured host's resolved addresses is the fast path. Beyond
// that, sources on a non-routable network are also allowed to try, because the
// configured peer is a *machine*, not an address, and a machine's other
// addresses cannot be derived from the one that was configured:
//
//	a dual-stack Windows host opens the connection from an IPv6 link-local
//	address that no lookup of its IPv4 will ever return
//	DHCP renewal, a second NIC, Wi-Fi/Ethernet failover and VPN routes all
//	change the source address without changing the peer
//
// Requiring an exact address match therefore rejects the legitimate peer: a
// host configured by its IPv4 address can never match the IPv6 link-local
// address it actually connects from, so every inbound attempt is refused and
// the link flaps between failed accepts and outbound retries.
//
// Globally routable sources are still refused before they can consume handshake
// work, so the internet cannot reach the handshake. The shared key remains the
// authentication control, exactly as before — this widens who may *attempt*
// authentication, never who passes it.
func isAllowedPeer(addr net.Addr, allowedIPs []net.IP) bool {
	// Resolution failed: no configured peer is known, so fail closed.
	if len(allowedIPs) == 0 {
		return false
	}

	remoteIP := remoteIP(addr)
	if remoteIP == nil {
		return false
	}
	for _, allowedIP := range allowedIPs {
		if allowedIP.Equal(remoteIP) {
			return true
		}
	}
	return isLocalNetwork(remoteIP)
}

// remoteIP extracts the IP from a net.Addr, tolerating both *net.TCPAddr and
// the host:port string form.
func remoteIP(addr net.Addr) net.IP {
	if tcpAddr, ok := addr.(*net.TCPAddr); ok {
		return tcpAddr.IP
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil
	}
	return net.ParseIP(host)
}

// isLocalNetwork reports whether ip is on a network that is not globally
// routable — RFC1918/ULA private space, or an IPv6 link-local address.
//
// Loopback is deliberately excluded: the peer is another machine, so a
// connection from this host is never the configured peer.
func isLocalNetwork(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// InboundFile handles a peer-initiated file transfer. The connection is
// identical to the control channel up to the IV exchange and distinguished only
// by the first packet it sends. The stream is positioned at the file header
// when this is called, and the connection is closed once it returns.
//
// A real MWB peer opens these against the clipboard port, which ListenFileChannel
// serves; the control listener keeps the same branch so that two Linux clients
// still interoperate over a single port.
//
// A nil handler rejects file connections, which is the behaviour before file
// transfer existed.
type InboundFile func(c *Conn, push bool)

// ListenAndAccept starts a TCP server on the given port and sends accepted
// control connections (after handshake) to the returned channel. This allows
// Windows MWB to connect TO us, which is faster after lock/reconnect cycles.
//
// allowedHost restricts inbound connections to the configured peer. Hostnames
// are resolved once at listener startup; resolution failure disables inbound
// connections while leaving the outbound retry path available.
func ListenAndAccept(port int, securityKey, machineName, allowedHost string, onFile InboundFile, stop chan struct{}) (chan *Conn, error) {
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

			// Refuse globally routable sources before they can consume handshake
			// work. The shared key remains the authentication control; this is
			// defense in depth.
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

				conn, err := newSecureStream(raw, securityKey, machineName, handshakeTimeout)
				if err != nil {
					slog.Error("incoming stream setup failed", "remote", raw.RemoteAddr(), "err", err)
					_ = raw.Close()
					return
				}

				// The first packet decides what this connection is. It must be
				// read before anything is sent: a file-sending peer reads our
				// first packet as its header and aborts on an unexpected type.
				first, err := conn.RecvPacket()
				if err != nil {
					slog.Error("incoming first packet failed", "remote", raw.RemoteAddr(), "err", err)
					_ = raw.Close()
					return
				}

				if first.Type == protocol.Clipboard || first.Type == protocol.ClipboardPush {
					if onFile == nil {
						slog.Debug("file transfer offered but no handler registered", "remote", raw.RemoteAddr())
						_ = raw.Close()
						return
					}
					// MWB's ShakeHand reads a 64-byte header from its peer in
					// both roles, so a pushing Windows client sits in ReadEx
					// until its 30s receive timeout unless this side answers,
					// and then reports the channel as rejected. Mirror what MWB
					// sends from the accepting side: a header naming us.
					ack := &protocol.Packet{Type: protocol.ClipboardPush, Src: conn.MachineID}
					ack.SetMachineName(machineName)
					if err := conn.sendChannelHeader(ack); err != nil {
						slog.Warn("file channel: could not acknowledge peer",
							"remote", raw.RemoteAddr(), "err", err)
						_ = raw.Close()
						return
					}

					if err := raw.SetDeadline(time.Time{}); err != nil {
						slog.Warn("clear file transfer deadline", "err", err)
					}
					onFile(conn, first.Type == protocol.ClipboardPush)
					_ = raw.Close()
					return
				}

				if err := conn.finishControlSetup(machineName, first); err != nil {
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

func (c *Conn) doHandshake(machineName string, first *protocol.Packet) error {
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
		pkt := first
		first = nil
		if pkt == nil {
			var err error
			if pkt, err = c.RecvPacket(); err != nil {
				return fmt.Errorf("recv: %w", err)
			}
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

// Reader exposes the decrypted stream. The file-transfer channel reads its
// header and body directly from here rather than as packets.
func (c *Conn) Reader() io.Reader { return c.dec }

// Writer exposes the encrypted stream for the same reason.
func (c *Conn) Writer() io.Writer { return c.enc }

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

// sendChannelHeader writes the raw DATA structure used by Clipboard.ShakeHand.
// Unlike control packets, this header does not carry the MWB magic/checksum
// stamp in bytes 1-3. Stamping it changes the uint32 PackageType from
// Clipboard/ClipboardPush into an unknown value and PowerToys rejects the
// channel before reading any file bytes.
func (c *Conn) sendChannelHeader(p *protocol.Packet) error {
	buf := p.Marshal()
	if len(buf) != protocol.PacketSizeEx {
		return fmt.Errorf("file channel header has %d bytes, want %d", len(buf), protocol.PacketSizeEx)
	}

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.raw.SetWriteDeadline(time.Now().Add(500 * time.Millisecond))
	n, err := c.enc.Write(buf)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	return nil
}

// recvChannelHeader reads the peer's half of the file channel handshake.
//
// ShakeHand writes that packet straight out of the DATA struct rather than
// through the control send path, so it carries no magic stamp and RecvPacket
// rejects it as a magic mismatch. Only the type, name and Src are meaningful
// here, and the channel is already authenticated by the shared key: an
// unstamped header cannot be read at all without it. Requiring the three bytes
// to be zero is also a regression guard against accidentally using SendPacket,
// which was the cause of outbound transfers being silently rejected.
func (c *Conn) recvChannelHeader() (*protocol.Packet, error) {
	buf := make([]byte, protocol.PacketSizeEx)
	if _, err := io.ReadFull(c.dec, buf); err != nil {
		return nil, fmt.Errorf("read channel header: %w", err)
	}
	if buf[1] != 0 || buf[2] != 0 || buf[3] != 0 {
		return nil, fmt.Errorf("file channel header is stamped: type word 0x%08x", binary.LittleEndian.Uint32(buf[:4]))
	}
	return protocol.UnmarshalPacket(buf)
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

// DialFile opens a file-transfer connection to the peer.
//
// addr must be the peer's *clipboard* port, which is the base port, not the
// control port. MWB runs skClipboardServer on TcpPort and skMessageServer on
// TcpPort+1; only the former routes a connection into ShakeHand and the file
// receiver. The control port answers with a Handshake instead and the transfer
// is written into a socket that never reads it.
//
// The IV exchange is the same as the control channel's, and the connection is
// then identified by its first packet, where the control channel sends
// Handshake.
//
// That packet also declares which way the bytes flow. MWB's accept side reads
// it as `clientPushData = package.Type == PackageType.ClipboardPush`, so
// ClipboardPush means the dialer sends and the peer reads, while Clipboard
// means the dialer is *asking* for the peer's clipboard and the peer sends —
// which is the pull that MWB's own GetRemoteClipboard performs. Announcing
// Clipboard while pushing leaves both ends waiting to read, and it fails
// silently: the sender still writes its file into the socket and logs success.
//
// The returned Conn is positioned for the caller to write the file header and
// body; the caller owns closing it.
func DialFile(addr, securityKey, machineName string, machineID uint32, timeout time.Duration) (*Conn, error) {
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial file channel: %w", err)
	}

	c, err := newSecureStream(raw, securityKey, machineName, timeout)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}

	hdr := &protocol.Packet{
		Type: protocol.ClipboardPush,
		Src:  machineID,
		// "Other" stages the received file in MWB's private directory and places
		// it on the Windows clipboard. The user can then paste it into any File
		// Explorer folder, which is the normal MWB copy/paste workflow.
		PostAction: protocol.PostActionOther,
	}
	hdr.SetMachineName(machineName)
	if err := c.sendChannelHeader(hdr); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("send file channel header: %w", err)
	}

	// MWB's ShakeHand writes its own header before it reads ours, so a reply is
	// already in flight. Reading it is the step the client is documented to
	// perform, and it is the only evidence this side can obtain that the peer
	// reached its clipboard accept path: everything after this point is a
	// one-way write that succeeds whether or not anyone is reading.
	//
	reply, err := c.recvChannelHeader()
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("receive file channel header: %w", err)
	}
	if reply.Type != protocol.Clipboard && reply.Type != protocol.ClipboardPush {
		_ = raw.Close()
		return nil, fmt.Errorf("receive file channel header: unexpected type %d", reply.Type)
	}
	c.RemoteName = reply.MachineName()
	c.RemoteID = reply.Src
	slog.Info("file channel accepted", "remote", c.RemoteName, "remoteID", c.RemoteID)

	// The transfer itself is unbounded in time relative to a handshake.
	if err := raw.SetDeadline(time.Time{}); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("clear file channel deadline: %w", err)
	}
	return c, nil
}
