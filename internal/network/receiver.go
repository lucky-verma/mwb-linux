// internal/network/receiver.go
package network

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// heartbeatEvery is how often a heartbeat goes out on conn. A link with an
// idle timeout (a Linux peer) sends five per timeout, so a dead peer is
// noticed quickly and a busy link is never mistaken for one. PowerToys links
// keep the interval MWB has always used.
func heartbeatEvery(conn *Conn) time.Duration {
	if conn.idleTimeout > 0 {
		return conn.idleTimeout / 5
	}
	return 5 * time.Second
}

// startHeartbeat sends periodic heartbeats to keep the connection alive.
// Windows MWB drops clients that don't send heartbeats within ~10s.
func startHeartbeat(conn *Conn, stop chan struct{}) {
	ticker := time.NewTicker(heartbeatEvery(conn))
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			hb := &protocol.Packet{
				Type: protocol.HeartbeatEx,
				Src:  conn.MachineID,
				Des:  protocol.IDAll,
			}
			hb.SetMachineName(conn.LocalName)
			if err := conn.SendPacket(hb); err != nil {
				slog.Debug("heartbeat send failed", "err", err)
				return
			}
		}
	}
}

// ReceiveLoop reads packets from the connection and dispatches them.
func ReceiveLoop(conn *Conn, handler *Handler) error {
	stop := make(chan struct{})
	go startHeartbeat(conn, stop)
	defer close(stop)

	for {
		if conn.idleTimeout > 0 {
			_ = conn.raw.SetReadDeadline(time.Now().Add(conn.idleTimeout))
		}
		pkt, err := conn.RecvPacket()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				slog.Info("connection closed by remote")
				return nil
			}
			var ne net.Error
			if conn.idleTimeout > 0 && errors.As(err, &ne) && ne.Timeout() {
				return fmt.Errorf("peer silent for %s; reconnecting", conn.idleTimeout)
			}
			return err
		}

		switch pkt.Type {
		case protocol.Hi:
			slog.Debug("Hi received, responding with Hello", "localName", conn.LocalName, "machineID", conn.MachineID)
			resp := &protocol.Packet{
				Type: protocol.Hello,
				Src:  conn.MachineID,
				Des:  pkt.Src,
			}
			resp.SetMachineName(conn.LocalName)
			if err := conn.SendPacket(resp); err != nil {
				slog.Error("send Hello response", "err", err)
			}
		case protocol.Heartbeat, protocol.HeartbeatEx, protocol.HeartbeatExL2, protocol.HeartbeatExL3:
			slog.Debug("heartbeat received", "type", pkt.Type, "from", pkt.MachineName())
			// Answer a machine's own heartbeat, which goes to everyone, but
			// never an answer, which goes to one machine: two mwb peers
			// would bounce it back and forth for as long as they stay
			// connected, and each new heartbeat would add another.
			if pkt.Des != protocol.IDAll {
				break
			}
			resp := &protocol.Packet{
				Type: pkt.Type,
				Src:  conn.MachineID,
				Des:  pkt.Src,
			}
			resp.SetMachineName(conn.LocalName)
			if err := conn.SendPacket(resp); err != nil {
				return fmt.Errorf("send heartbeat response: %w", err)
			}
		case protocol.ByeBye:
			slog.Info("remote disconnected (ByeBye)")
			return nil
		case protocol.Invalid:
			slog.Warn("invalid packet received")
		case protocol.Handshake:
			slog.Debug("late handshake packet, ignoring")
		default:
			// Handle Matrix packets (bit 128 set) — server sends these for machine layout
			if pkt.Type&protocol.Matrix == protocol.Matrix {
				subType := pkt.Type &^ protocol.Matrix
				slog.Debug("matrix packet received", "fullType", pkt.Type, "subType", subType, "src", pkt.Src)
				if subType == protocol.Hi {
					resp := &protocol.Packet{
						Type: protocol.Hello,
						Src:  conn.MachineID,
						Des:  pkt.Src,
					}
					resp.SetMachineName(conn.LocalName)
					if err := conn.SendPacket(resp); err != nil {
						slog.Error("send matrix Hello response", "err", err)
					}
				}
			} else {
				handler.HandlePacket(pkt)
			}
		}
	}
}
