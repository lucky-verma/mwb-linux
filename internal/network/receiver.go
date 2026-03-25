// internal/network/receiver.go
package network

import (
	"errors"
	"io"
	"log/slog"

	"github.com/bketelsen/mwb/internal/protocol"
)

// ReceiveLoop reads packets from the connection and dispatches them.
func ReceiveLoop(conn *Conn, handler *Handler) error {
	for {
		pkt, err := conn.RecvPacket()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				slog.Info("connection closed by remote")
				return nil
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
			resp := &protocol.Packet{
				Type: pkt.Type,
				Src:  conn.MachineID,
				Des:  pkt.Src,
			}
			resp.SetMachineName(conn.LocalName)
			if err := conn.SendPacket(resp); err != nil {
				slog.Error("send heartbeat response", "err", err)
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
