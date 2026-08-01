// internal/protocol/packet.go
package protocol

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// MouseData matches MWB MOUSEDATA struct at packet offset 16.
type MouseData struct {
	X          int32
	Y          int32
	WheelDelta int32
	DwFlags    int32
}

// KeyboardData matches MWB KEYBDDATA struct.
// DateTime at offset 16, wVk at offset 24, dwFlags at offset 28.
type KeyboardData struct {
	DateTime int64
	WVk      int32
	DwFlags  int32
}

// HandshakeData holds Machine1-4 at offsets 16-28.
type HandshakeData struct {
	Machine1 uint32
	Machine2 uint32
	Machine3 uint32
	Machine4 uint32
}

// Packet represents a decoded MWB packet.
// The payload union is represented by separate fields; only one is active
// depending on Type.
type Packet struct {
	Type PackageType
	ID   int32
	Src  uint32
	Des  uint32

	// Payload union (only one active based on Type)
	Mouse         MouseData
	Keyboard      KeyboardData
	Handshake     HandshakeData
	ClipboardData []byte // 48 bytes of clipboard chunk data (bytes 16-63)

	// PostAction shares offset 16 with the rest of the union. It is only
	// meaningful on the packet that opens a file channel, which carries no
	// clipboard bytes; on a data-carrying clipboard packet those same four
	// bytes are payload, and this field is whatever they happened to be.
	PostAction ClipboardPostAction

	// Extended area (bytes 32-63, only for big packets)
	machineName [32]byte
}

// SetMachineName sets the machine name (ASCII, max 32 chars, space-padded).
func (p *Packet) SetMachineName(name string) {
	for i := range p.machineName {
		p.machineName[i] = ' '
	}
	copy(p.machineName[:], []byte(name))
}

// MachineName returns the machine name, trimmed of spaces.
func (p *Packet) MachineName() string {
	return strings.TrimRight(string(p.machineName[:]), " ")
}

// Marshal serializes the packet to its wire format (32 or 64 bytes).
// Does NOT set checksum or magic number (that happens at send time).
func (p *Packet) Marshal() []byte {
	big := IsBigPacket(p.Type)
	size := PacketSize
	if big {
		size = PacketSizeEx
	}

	buf := make([]byte, size)

	// Header: bytes 0-15
	buf[0] = byte(p.Type)
	// bytes 1-3 reserved for checksum/magic (set at send time)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(p.ID))
	binary.LittleEndian.PutUint32(buf[8:12], p.Src)
	binary.LittleEndian.PutUint32(buf[12:16], p.Des)

	// Payload union: bytes 16-31
	switch p.Type {
	case Mouse, NextMachine, HideMouse:
		binary.LittleEndian.PutUint32(buf[16:20], uint32(p.Mouse.X))
		binary.LittleEndian.PutUint32(buf[20:24], uint32(p.Mouse.Y))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(p.Mouse.WheelDelta))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(p.Mouse.DwFlags))
	case Keyboard:
		binary.LittleEndian.PutUint64(buf[16:24], uint64(p.Keyboard.DateTime))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(p.Keyboard.WVk))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(p.Keyboard.DwFlags))
	case Handshake, HandshakeAck:
		binary.LittleEndian.PutUint32(buf[16:20], p.Handshake.Machine1)
		binary.LittleEndian.PutUint32(buf[20:24], p.Handshake.Machine2)
		binary.LittleEndian.PutUint32(buf[24:28], p.Handshake.Machine3)
		binary.LittleEndian.PutUint32(buf[28:32], p.Handshake.Machine4)
	case ClipboardText, ClipboardImage, ClipboardDataEnd,
		Clipboard, ClipboardAsk, ClipboardPush:
		// Clipboard packets: bytes 16-63 are raw clipboard data (48 bytes)
		if p.ClipboardData != nil {
			n := len(p.ClipboardData)
			if n > 48 {
				n = 48
			}
			copy(buf[16:16+n], p.ClipboardData[:n])
		} else {
			// No payload means this packet opens a file channel rather than
			// carrying clipboard bytes, so the union holds PostAction. MWB
			// picks the destination folder from it.
			binary.LittleEndian.PutUint32(buf[16:20], uint32(p.PostAction))
		}
	}

	// Extended area: bytes 32-63 (machine name for big packets, BUT not for clipboard)
	if big && p.ClipboardData == nil {
		copy(buf[32:64], p.machineName[:])
	}

	return buf
}

// UnmarshalPacket decodes a raw packet buffer (32 or 64 bytes).
// Assumes bytes 1-3 have already been cleared after checksum/magic validation.
func UnmarshalPacket(buf []byte) (*Packet, error) {
	if len(buf) < PacketSize {
		return nil, fmt.Errorf("packet too short: %d bytes", len(buf))
	}

	p := &Packet{
		Type: PackageType(buf[0]),
		ID:   int32(binary.LittleEndian.Uint32(buf[4:8])),
		Src:  binary.LittleEndian.Uint32(buf[8:12]),
		Des:  binary.LittleEndian.Uint32(buf[12:16]),
	}

	switch p.Type {
	case Mouse, NextMachine, HideMouse:
		p.Mouse.X = int32(binary.LittleEndian.Uint32(buf[16:20]))
		p.Mouse.Y = int32(binary.LittleEndian.Uint32(buf[20:24]))
		p.Mouse.WheelDelta = int32(binary.LittleEndian.Uint32(buf[24:28]))
		p.Mouse.DwFlags = int32(binary.LittleEndian.Uint32(buf[28:32]))
	case Keyboard:
		p.Keyboard.DateTime = int64(binary.LittleEndian.Uint64(buf[16:24]))
		p.Keyboard.WVk = int32(binary.LittleEndian.Uint32(buf[24:28]))
		p.Keyboard.DwFlags = int32(binary.LittleEndian.Uint32(buf[28:32]))
	case Handshake, HandshakeAck:
		p.Handshake.Machine1 = binary.LittleEndian.Uint32(buf[16:20])
		p.Handshake.Machine2 = binary.LittleEndian.Uint32(buf[20:24])
		p.Handshake.Machine3 = binary.LittleEndian.Uint32(buf[24:28])
		p.Handshake.Machine4 = binary.LittleEndian.Uint32(buf[28:32])
	case ClipboardText, ClipboardImage, ClipboardDataEnd,
		Clipboard, ClipboardAsk, ClipboardPush:
		// Clipboard: bytes 16-63 are raw data (48 bytes for big packets)
		end := len(buf)
		if end > 64 {
			end = 64
		}
		if end > 16 {
			p.ClipboardData = make([]byte, end-16)
			copy(p.ClipboardData, buf[16:end])
			// Same four bytes, read both ways: payload here, PostAction on the
			// packet that opens a file channel. The caller knows which it has.
			p.PostAction = ClipboardPostAction(binary.LittleEndian.Uint32(buf[16:20]))
		}
	}

	// Extended area for big packets. On a clipboard packet these bytes are
	// payload rather than a name — the same overlap as offset 16, since the
	// packet that opens a file channel names its sender there and a
	// data-carrying one does not. Decoding both costs nothing, and the caller
	// knows which kind it is holding. Skipping this for clipboard types left
	// the name on a file channel's opening packet permanently unreadable.
	if IsBigPacket(p.Type) && len(buf) >= PacketSizeEx {
		copy(p.machineName[:], buf[32:64])
	}

	return p, nil
}
