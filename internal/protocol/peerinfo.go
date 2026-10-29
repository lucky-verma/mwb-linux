// internal/protocol/peerinfo.go
package protocol

// PeerInfo is what two mwb-linux peers tell each other right after they
// connect, so neither user has to type the other's settings: PowerToys
// exchanges the equivalent through its own machine matrix.
//
// It travels in a LinuxPeerInfo packet, reusing the 16-byte MOUSEDATA
// payload, as NextMachine already does for its target ID:
//
//	X          screen width in pixels, 0 if unknown
//	Y          screen height in pixels, 0 if unknown
//	WheelDelta flags: bit 0 = sharing active, bit 1 = peer is on the right
//	DwFlags    bits 0-7: format version; bits 8-31 (version 2): EdgeAge
type PeerInfo struct {
	ScreenWidth  int32
	ScreenHeight int32
	// Edge is the side of the sender's screen the receiver sits on:
	// "left" or "right". In a correct layout the two edges are opposite.
	Edge string
	// Sharing reports that the sender actually forwards its own input when
	// the cursor crosses: bidirectional is on and capture started.
	Sharing bool
	// EdgeAge is how many minutes ago the sender's Edge was last chosen,
	// capped at EdgeAgeUnknown, which also means "never recorded". An age
	// rather than a time, so the two machines' clocks need not agree. When
	// both put the other on the same side, the more recent choice wins.
	EdgeAge uint32
}

// EdgeAgeUnknown is the EdgeAge of a side that was never recorded, and the
// largest age that fits on the wire.
const EdgeAgeUnknown = 1<<24 - 1

const (
	peerInfoVersion     = 2
	peerInfoSharing     = 1 << 0
	peerInfoEdgeRight   = 1 << 1
	peerInfoMaxScreenPx = 65535
)

// NewPeerInfoPacket builds a LinuxPeerInfo packet.
func NewPeerInfoPacket(src, des uint32, info PeerInfo) *Packet {
	p := &Packet{Type: LinuxPeerInfo, Src: src, Des: des}
	if validScreen(info.ScreenWidth, info.ScreenHeight) {
		p.Mouse.X, p.Mouse.Y = info.ScreenWidth, info.ScreenHeight
	}
	var flags int32
	if info.Sharing {
		flags |= peerInfoSharing
	}
	if info.Edge == "right" {
		flags |= peerInfoEdgeRight
	}
	p.Mouse.WheelDelta = flags
	age := min(info.EdgeAge, EdgeAgeUnknown)
	p.Mouse.DwFlags = int32(uint32(peerInfoVersion) | age<<8)
	return p
}

// PeerInfo decodes a LinuxPeerInfo packet. ok is false for any other packet
// type or an unknown format version. An implausible screen size is reported
// as unknown (0×0) rather than trusted. Version 1 carries no edge age.
func (p *Packet) PeerInfo() (info PeerInfo, ok bool) {
	if p.Type != LinuxPeerInfo {
		return PeerInfo{}, false
	}
	switch raw := uint32(p.Mouse.DwFlags); {
	case raw == 1:
		info.EdgeAge = EdgeAgeUnknown
	case raw&0xff == peerInfoVersion:
		info.EdgeAge = raw >> 8
	default:
		return PeerInfo{}, false
	}
	if validScreen(p.Mouse.X, p.Mouse.Y) {
		info.ScreenWidth, info.ScreenHeight = p.Mouse.X, p.Mouse.Y
	}
	info.Sharing = p.Mouse.WheelDelta&peerInfoSharing != 0
	info.Edge = "left"
	if p.Mouse.WheelDelta&peerInfoEdgeRight != 0 {
		info.Edge = "right"
	}
	return info, true
}

func validScreen(w, h int32) bool {
	return w > 0 && h > 0 && w <= peerInfoMaxScreenPx && h <= peerInfoMaxScreenPx
}
