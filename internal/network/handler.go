// internal/network/handler.go
package network

import (
	"log/slog"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// MouseDevice is the interface for mouse injection.
type MouseDevice interface {
	MoveTo(x, y int32) error
	ButtonDown(button uint16) error
	ButtonUp(button uint16) error
	Wheel(delta int32) error
	HWheel(delta int32) error
}

// KeyboardDevice is the interface for keyboard injection.
type KeyboardDevice interface {
	KeyDown(code uint16) error
	KeyUp(code uint16) error
}

// ClipboardHandler handles clipboard packets.
type ClipboardHandler interface {
	HandlePacket(pkt *protocol.Packet)
}

// Handler processes incoming MWB packets and injects input events.
type Handler struct {
	Mouse       MouseDevice
	Keyboard    KeyboardDevice
	Clipboard   ClipboardHandler // optional clipboard handler
	OnActivated func()           // called when remote sends MachineSwitched
	OnReclaimed func()           // called when server sends NextMachine (cursor bounced back)
	ActivatedAt *time.Time       // when cursor last arrived — skip mouse injection briefly

	// InboundMultiplier scales Windows->Linux cursor movement. 1.0 = mirror
	// Windows 1:1 (the default). Inbound is absolute, so this applies a constant
	// gain to per-packet deltas rather than a true acceleration curve.
	InboundMultiplier float64
	// inbound cursor tracking (single-goroutine receive loop, no lock needed)
	inX, inY         int32 // current injected absolute position (0-65535)
	lastInX, lastInY int32 // last absolute position reported by the remote
	inSeeded         bool  // false until the first inbound move seeds the tracker
}

// HandlePacket dispatches a packet to the appropriate handler.
func (h *Handler) HandlePacket(pkt *protocol.Packet) {
	switch pkt.Type {
	case protocol.Mouse:
		h.handleMouse(pkt)
	case protocol.Keyboard:
		h.handleKeyboard(pkt)
	case protocol.MachineSwitched:
		slog.Info("MachineSwitched: cursor switched to us", "src", pkt.Src)
		now := time.Now()
		h.ActivatedAt = &now
		h.inSeeded = false // reseed inbound tracker so the entry move snaps, not scales
		if h.OnActivated != nil {
			h.OnActivated()
		}
	case protocol.HideMouse:
		slog.Debug("HideMouse received — cursor leaving us", "src", pkt.Src)
	case protocol.NextMachine:
		slog.Info("NextMachine received — server wants us to take cursor back",
			"src", pkt.Src, "des", pkt.Des, "targetID", pkt.Mouse.WheelDelta)
		// Server's cursor hit an edge toward us — reclaim local control
		if h.OnReclaimed != nil {
			h.OnReclaimed()
		}
	case protocol.ClipboardText, protocol.ClipboardImage, protocol.ClipboardDataEnd,
		protocol.Clipboard, protocol.ClipboardAsk, protocol.ClipboardPush:
		if h.Clipboard != nil {
			h.Clipboard.HandlePacket(pkt)
		}
	case protocol.Hello, protocol.Awake, protocol.HandshakeAck:
		// expected control packets — ignore silently
	default:
		slog.Debug("unhandled packet type", "type", pkt.Type)
	}
}

func (h *Handler) handleMouse(pkt *protocol.Packet) {
	md := pkt.Mouse
	var err error
	switch int(md.DwFlags) {
	case protocol.WM_MOUSEMOVE:
		err = h.moveInbound(md.X, md.Y)
	case protocol.WM_LBUTTONDOWN:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_LEFT)
		}
	case protocol.WM_LBUTTONUP:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_LEFT)
		}
	case protocol.WM_RBUTTONDOWN:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_RIGHT)
		}
	case protocol.WM_RBUTTONUP:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_RIGHT)
		}
	case protocol.WM_MBUTTONDOWN:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonDown(input.BTN_MIDDLE)
		}
	case protocol.WM_MBUTTONUP:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			err = h.Mouse.ButtonUp(input.BTN_MIDDLE)
		}
	case protocol.WM_XBUTTONDOWN:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			// WheelDelta holds which X-button: 1=XBUTTON1 (BTN_SIDE), 2=XBUTTON2 (BTN_EXTRA)
			btn := uint16(input.BTN_SIDE)
			if md.WheelDelta == 2 {
				btn = input.BTN_EXTRA
			}
			err = h.Mouse.ButtonDown(btn)
		}
	case protocol.WM_XBUTTONUP:
		if err = h.moveInbound(md.X, md.Y); err == nil {
			btn := uint16(input.BTN_SIDE)
			if md.WheelDelta == 2 {
				btn = input.BTN_EXTRA
			}
			err = h.Mouse.ButtonUp(btn)
		}
	case protocol.WM_MOUSEWHEEL:
		delta := md.WheelDelta / 120
		if delta == 0 && md.WheelDelta > 0 {
			delta = 1
		} else if delta == 0 && md.WheelDelta < 0 {
			delta = -1
		}
		err = h.Mouse.Wheel(delta)
	case protocol.WM_MOUSEHWHEEL:
		delta := md.WheelDelta / 120
		if delta == 0 && md.WheelDelta > 0 {
			delta = 1
		} else if delta == 0 && md.WheelDelta < 0 {
			delta = -1
		}
		err = h.Mouse.HWheel(delta)
	default:
		slog.Debug("unhandled mouse event", "flags", md.DwFlags)
		return
	}
	if err != nil {
		slog.Error("mouse input error", "err", err)
	}
}

// inboundJumpThreshold (in 0-65535 space) separates ordinary per-packet motion
// from a teleport (e.g. the cursor entering from a screen edge). A jump is
// snapped to the reported position rather than scaled, so switch-in never sends
// the cursor flying.
const inboundJumpThreshold = 4096

// moveInbound injects an inbound (Windows->Linux) absolute cursor update,
// applying InboundMultiplier as a constant gain. With multiplier 1.0 it tracks
// the remote position exactly (identical to a 1:1 mirror).
func (h *Handler) moveInbound(mdX, mdY int32) error {
	mult := h.InboundMultiplier
	if mult <= 0 {
		mult = 1.0
	}
	jump := !h.inSeeded ||
		abs32(mdX-h.lastInX) > inboundJumpThreshold ||
		abs32(mdY-h.lastInY) > inboundJumpThreshold
	if jump || mult == 1.0 {
		// Snap to the reported position: on a teleport, or when no gain is asked
		// for (keeps absolute mirroring exact and drift-free at multiplier 1.0).
		h.inX, h.inY = mdX, mdY
	} else {
		h.inX = clamp65535(h.inX + int32(float64(mdX-h.lastInX)*mult))
		h.inY = clamp65535(h.inY + int32(float64(mdY-h.lastInY)*mult))
	}
	h.lastInX, h.lastInY = mdX, mdY
	h.inSeeded = true
	return h.Mouse.MoveTo(h.inX, h.inY)
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func clamp65535(v int32) int32 {
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return v
}

func (h *Handler) handleKeyboard(pkt *protocol.Packet) {
	kd := pkt.Keyboard
	keyCode, ok := input.VKToKeyCode(kd.WVk)
	if !ok {
		slog.Debug("unknown VK code", "vk", kd.WVk)
		return
	}
	var err error
	isUp := (kd.DwFlags & protocol.LLKHF_UP) != 0
	if isUp {
		err = h.Keyboard.KeyUp(keyCode)
	} else {
		err = h.Keyboard.KeyDown(keyCode)
	}
	if err != nil {
		slog.Error("keyboard input error", "err", err)
	}
}
