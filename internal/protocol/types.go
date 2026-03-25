// internal/protocol/types.go
package protocol

// PackageType identifies the type of MWB packet.
type PackageType byte

const (
	Hi                   PackageType = 2
	Hello                PackageType = 3
	ByeBye               PackageType = 4
	Heartbeat            PackageType = 20
	Awake                PackageType = 21
	HideMouse            PackageType = 50
	HeartbeatEx          PackageType = 51
	HeartbeatExL2        PackageType = 52
	HeartbeatExL3        PackageType = 53
	Clipboard            PackageType = 69
	ClipboardDragDrop    PackageType = 70
	ClipboardDragDropEnd PackageType = 71
	ExplorerDragDrop     PackageType = 72
	ClipboardCapture     PackageType = 73
	CaptureScreenCommand PackageType = 74
	ClipboardDragDropOp  PackageType = 75
	ClipboardDataEnd     PackageType = 76
	MachineSwitched      PackageType = 77
	ClipboardAsk         PackageType = 78
	ClipboardPush        PackageType = 79
	NextMachine          PackageType = 121
	Keyboard             PackageType = 122
	Mouse                PackageType = 123
	ClipboardText        PackageType = 124
	ClipboardImage       PackageType = 125
	Handshake            PackageType = 126
	HandshakeAck         PackageType = 127
	Matrix               PackageType = 128
	Invalid              PackageType = 0xFF
	Error                PackageType = 0xFE
)

const (
	PacketSize   = 32
	PacketSizeEx = 64
)

// WM_* mouse message constants used as MOUSEDATA.dwFlags on the wire.
const (
	WM_MOUSEMOVE   = 0x0200
	WM_LBUTTONDOWN = 0x0201
	WM_LBUTTONUP   = 0x0202
	WM_RBUTTONDOWN = 0x0204
	WM_RBUTTONUP   = 0x0205
	WM_MBUTTONDOWN = 0x0207
	WM_MBUTTONUP   = 0x0208
	WM_MOUSEWHEEL  = 0x020A
	WM_XBUTTONDOWN = 0x020B
	WM_XBUTTONUP   = 0x020C
	WM_MOUSEHWHEEL = 0x020E
)

// LLKHF keyboard flag bits used as KEYBDDATA.dwFlags on the wire.
const (
	LLKHF_EXTENDED = 0x01
	LLKHF_UP       = 0x80
)

// IDAll is the broadcast destination (Des=255 means "all machines").
const IDAll = 255

// MoveMouseRelative is the offset added to X/Y to signal relative mouse movement.
const MoveMouseRelative = 100000

// IsBigPacket returns true if the packet type uses 64-byte extended format.
func IsBigPacket(pt PackageType) bool {
	switch pt {
	case Hello, Awake, Heartbeat, HeartbeatEx,
		Handshake, HandshakeAck,
		ClipboardPush, Clipboard, ClipboardAsk,
		ClipboardImage, ClipboardText, ClipboardDataEnd:
		return true
	}
	// Matrix types have bit 128 set
	if pt&Matrix == Matrix {
		return true
	}
	return false
}
