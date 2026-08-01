// internal/protocol/packet_test.go
package protocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// The packet that opens a file channel carries no clipboard bytes, so MWB reads
// the union at offset 16 as ClipboardPostAction and picks the destination
// folder from it. The machine name still has to land at 32-63: MWB rejects the
// channel unless it can resolve that name back to the ID in Src.
func TestMarshal_FileChannelHeaderCarriesPostAction(t *testing.T) {
	p := &Packet{Type: ClipboardPush, Src: 0x11223344, PostAction: PostActionDesktop}
	p.SetMachineName("thinkersloop")

	buf := p.Marshal()
	if len(buf) != PacketSizeEx {
		t.Fatalf("len = %d, want %d", len(buf), PacketSizeEx)
	}
	if got := binary.LittleEndian.Uint32(buf[16:20]); got != uint32(PostActionDesktop) {
		t.Errorf("PostAction at offset 16 = %d, want %d", got, PostActionDesktop)
	}
	if got := strings.TrimRight(string(buf[32:64]), " "); got != "thinkersloop" {
		t.Errorf("machine name at 32-63 = %q, want thinkersloop", got)
	}

	back, err := UnmarshalPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if back.PostAction != PostActionDesktop {
		t.Errorf("round-tripped PostAction = %d, want %d", back.PostAction, PostActionDesktop)
	}
}

// PostAction shares its offset with real clipboard payload, so writing it must
// stay confined to the packet that has no payload to lose.
func TestMarshal_ClipboardPayloadSurvivesPostActionOffset(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, 48)
	buf := (&Packet{Type: ClipboardText, ClipboardData: data}).Marshal()
	if !bytes.Equal(buf[16:64], data) {
		t.Errorf("payload at 16-63 = %x, want %x", buf[16:64], data)
	}
}

func TestPacketMarshalRoundTrip(t *testing.T) {
	p := &Packet{
		Type: Mouse,
		ID:   42,
		Src:  1,
		Des:  2,
	}
	p.Mouse.X = 32768
	p.Mouse.Y = 16384
	p.Mouse.WheelDelta = 0
	p.Mouse.DwFlags = WM_MOUSEMOVE

	buf := p.Marshal()
	if len(buf) != PacketSize {
		t.Fatalf("marshal len = %d, want %d", len(buf), PacketSize)
	}

	p2, err := UnmarshalPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Type != Mouse {
		t.Errorf("type = %d, want %d", p2.Type, Mouse)
	}
	if p2.ID != 42 {
		t.Errorf("id = %d, want 42", p2.ID)
	}
	if p2.Mouse.X != 32768 || p2.Mouse.Y != 16384 {
		t.Errorf("mouse = (%d,%d), want (32768,16384)", p2.Mouse.X, p2.Mouse.Y)
	}
}

func TestPacketMarshalBigPacket(t *testing.T) {
	p := &Packet{
		Type: Handshake,
		ID:   1,
		Src:  1,
		Des:  0,
	}
	p.SetMachineName("mylinuxbox")
	p.Handshake.Machine1 = 0xDEADBEEF
	p.Handshake.Machine2 = 0xCAFEBABE
	p.Handshake.Machine3 = 0x12345678
	p.Handshake.Machine4 = 0xABCD0123

	buf := p.Marshal()
	if len(buf) != PacketSizeEx {
		t.Fatalf("marshal len = %d, want %d", len(buf), PacketSizeEx)
	}

	p2, err := UnmarshalPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	name := p2.MachineName()
	if name != "mylinuxbox" {
		t.Errorf("machine name = %q, want %q", name, "mylinuxbox")
	}
	if p2.Handshake.Machine1 != 0xDEADBEEF {
		t.Errorf("machine1 = 0x%X, want 0xDEADBEEF", p2.Handshake.Machine1)
	}
}

func TestMachineNamePadding(t *testing.T) {
	p := &Packet{Type: Handshake}
	p.SetMachineName("ab")
	buf := p.Marshal()
	// Machine name is at bytes 32-63, ASCII space-padded to 32 bytes
	nameBytes := buf[32:64]
	if nameBytes[0] != 'a' || nameBytes[1] != 'b' || nameBytes[2] != ' ' {
		t.Errorf("name bytes = %v, want 'ab' + spaces", nameBytes[:4])
	}

	p2, _ := UnmarshalPacket(buf)
	if p2.MachineName() != "ab" {
		t.Errorf("got %q, want %q", p2.MachineName(), "ab")
	}
}

func TestPacketFieldOffsets(t *testing.T) {
	// Verify exact byte layout matches MWB C# StructLayout
	p := &Packet{Type: Keyboard, ID: 0, Src: 0, Des: 0}
	p.Keyboard.DateTime = 0x0102030405060708
	p.Keyboard.WVk = 0x41 // VK_A
	p.Keyboard.DwFlags = 0

	buf := p.Marshal()
	// DateTime at offset 16 (8 bytes, little-endian)
	dt := binary.LittleEndian.Uint64(buf[16:24])
	if dt != 0x0102030405060708 {
		t.Errorf("datetime at offset 16 = 0x%X, want 0x0102030405060708", dt)
	}
	// wVk at offset 24 (4 bytes)
	vk := binary.LittleEndian.Uint32(buf[24:28])
	if vk != 0x41 {
		t.Errorf("wVk at offset 24 = 0x%X, want 0x41", vk)
	}
}
