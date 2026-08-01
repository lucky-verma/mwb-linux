// internal/network/filelisten.go
package network

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// ListenFileChannel serves inbound file transfers on the clipboard port.
//
// MWB opens these against skClipboardServer, which listens on the base port,
// so they never arrive at the control listener on base+1 and a client that has
// only the control port open is never offered a file at all. Nothing else
// arrives here: the control handshake has its own port and its own server.
//
// allowedHost restricts connections to the configured peer, resolved once at
// startup, exactly as the control listener does.
func ListenFileChannel(port int, securityKey, machineName, allowedHost string, activeMachineID func() uint32, onFile InboundFile, stop chan struct{}) error {
	if onFile == nil {
		return fmt.Errorf("file channel listener needs a handler")
	}
	if activeMachineID == nil {
		return fmt.Errorf("file channel listener needs the active control machine ID")
	}

	allowedIPs, resolveErr := resolveAllowedPeer(allowedHost)
	if resolveErr != nil {
		slog.Warn("could not resolve configured peer; inbound file transfers disabled",
			"host", allowedHost, "err", resolveErr)
	}

	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	go func() {
		defer func() { _ = ln.Close() }()
		var transfers sync.WaitGroup
		defer transfers.Wait()
		pending := make(chan struct{}, maxPendingHandshakes)

		slog.Info("listening for inbound file transfers",
			"port", port, "allowedHost", allowedHost, "allowedIPs", allowedIPs)

		for {
			select {
			case <-stop:
				return
			default:
			}

			if tl, ok := ln.(*net.TCPListener); ok {
				_ = tl.SetDeadline(time.Now().Add(1 * time.Second))
			}

			raw, err := ln.Accept()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				slog.Debug("file listener accept error", "err", err)
				continue
			}

			if !isAllowedPeer(raw.RemoteAddr(), allowedIPs) {
				slog.Warn("rejected inbound file connection from unexpected source",
					"remote", raw.RemoteAddr(), "allowedHost", allowedHost)
				_ = raw.Close()
				continue
			}

			// Share the control listener's budget shape: a peer must not be
			// able to occupy unbounded goroutines by opening file channels.
			select {
			case pending <- struct{}{}:
			default:
				slog.Warn("rejected inbound file connection: too many in flight",
					"remote", raw.RemoteAddr())
				_ = raw.Close()
				continue
			}

			transfers.Add(1)
			go func(raw net.Conn) {
				defer transfers.Done()
				defer func() { <-pending }()
				defer func() { _ = raw.Close() }()
				serveFileChannel(raw, securityKey, machineName, activeMachineID, onFile)
			}(raw)
		}
	}()

	return nil
}

// serveFileChannel completes the handshake on one inbound file connection and
// hands the positioned stream to the caller's handler.
func serveFileChannel(raw net.Conn, securityKey, machineName string, activeMachineID func() uint32, onFile InboundFile) {
	conn, err := newSecureStream(raw, securityKey, machineName, handshakeTimeout)
	if err != nil {
		slog.Error("file channel stream setup failed", "remote", raw.RemoteAddr(), "err", err)
		return
	}

	first, err := conn.recvChannelHeader()
	if err != nil {
		slog.Error("file channel header failed", "remote", raw.RemoteAddr(), "err", err)
		return
	}
	if first.Type != protocol.Clipboard && first.Type != protocol.ClipboardPush {
		slog.Warn("unexpected packet type opening a file channel",
			"remote", raw.RemoteAddr(), "type", first.Type)
		return
	}

	// MWB's ShakeHand reads a header back before it sends anything, in both
	// roles. Without this the peer sits in ReadEx until its receive timeout and
	// then reports the channel as rejected.
	controlID := activeMachineID()
	if controlID == 0 {
		slog.Warn("file channel rejected: no active control connection", "remote", raw.RemoteAddr())
		return
	}
	// PowerToys requires ResolveID(name) == Src and IsConnectedTo(Src). The
	// random identity created for this file socket is not in its machine pool;
	// the header must reuse the active control connection's identity.
	conn.MachineID = controlID
	ack := &protocol.Packet{Type: protocol.ClipboardPush, Src: controlID}
	ack.SetMachineName(machineName)
	if err := conn.sendChannelHeader(ack); err != nil {
		slog.Warn("file channel: could not acknowledge peer",
			"remote", raw.RemoteAddr(), "err", err)
		return
	}

	slog.Info("inbound file channel accepted",
		"remote", first.MachineName(), "type", first.Type)

	if err := raw.SetDeadline(time.Time{}); err != nil {
		slog.Warn("clear file transfer deadline", "err", err)
	}
	onFile(conn, first.Type == protocol.ClipboardPush)
}
