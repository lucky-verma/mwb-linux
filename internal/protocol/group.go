// internal/protocol/group.go
package protocol

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
)

// A group of up to six mwb-linux machines shares two kinds of state:
//
//   - a group document (layout, members and their addresses, each machine's
//     screen), JSON encoded by the group package and sent in LinuxGroup
//     chunks, since it does not fit one packet;
//   - where the single shared cursor is, sent in one LinuxCursor packet.
//
// Both reuse the 16-byte MOUSEDATA payload, as LinuxPeerInfo does.

// GroupChunkData is how many document bytes one LinuxGroup packet carries.
const GroupChunkData = 12

// MaxGroupDocument is the largest document that fits the chunk numbering.
const MaxGroupDocument = 255 * GroupChunkData

// GroupChunks splits doc into LinuxGroup packets. msg tells a receiver which
// chunks belong together; use a different value for each document sent.
func GroupChunks(src, des uint32, msg byte, doc []byte) ([]*Packet, error) {
	if len(doc) == 0 || len(doc) > MaxGroupDocument {
		return nil, errors.New("group document size out of range")
	}
	total := (len(doc) + GroupChunkData - 1) / GroupChunkData
	pkts := make([]*Packet, 0, total)
	for i := 0; i < total; i++ {
		part := doc[i*GroupChunkData : min(len(doc), (i+1)*GroupChunkData)]
		var b [16]byte
		b[0], b[1], b[2], b[3] = msg, byte(i), byte(total), byte(len(part))
		copy(b[4:], part)
		p := &Packet{Type: LinuxGroup, Src: src, Des: des}
		p.Mouse = MouseData{
			X:          int32(binary.LittleEndian.Uint32(b[0:4])),
			Y:          int32(binary.LittleEndian.Uint32(b[4:8])),
			WheelDelta: int32(binary.LittleEndian.Uint32(b[8:12])),
			DwFlags:    int32(binary.LittleEndian.Uint32(b[12:16])),
		}
		pkts = append(pkts, p)
	}
	return pkts, nil
}

// GroupAssembler rebuilds documents from LinuxGroup packets arriving on one
// connection, in order. The zero value is ready to use.
type GroupAssembler struct {
	msg   byte
	next  int
	total int
	buf   []byte
}

// Add takes one LinuxGroup packet and returns a whole document once its
// last chunk arrives. A chunk out of sequence drops the partial document.
func (a *GroupAssembler) Add(p *Packet) (doc []byte, done bool) {
	if p.Type != LinuxGroup {
		return nil, false
	}
	var b [16]byte
	binary.LittleEndian.PutUint32(b[0:4], uint32(p.Mouse.X))
	binary.LittleEndian.PutUint32(b[4:8], uint32(p.Mouse.Y))
	binary.LittleEndian.PutUint32(b[8:12], uint32(p.Mouse.WheelDelta))
	binary.LittleEndian.PutUint32(b[12:16], uint32(p.Mouse.DwFlags))
	msg, idx, total, n := b[0], int(b[1]), int(b[2]), int(b[3])
	if total == 0 || idx >= total || n > GroupChunkData {
		a.buf, a.next = nil, 0
		return nil, false
	}
	if idx == 0 {
		a.msg, a.total, a.next, a.buf = msg, total, 0, make([]byte, 0, total*GroupChunkData)
	}
	if msg != a.msg || idx != a.next || total != a.total || a.buf == nil {
		a.buf, a.next = nil, 0
		return nil, false
	}
	a.buf = append(a.buf, b[4:4+n]...)
	a.next++
	if a.next < a.total {
		return nil, false
	}
	doc, a.buf, a.next = a.buf, nil, 0
	return doc, true
}

// CursorAt says the shared cursor moved to the machine whose name hashes to
// Target. Epoch orders announcements: everyone follows the highest one, so a
// late message from a machine that lost the cursor changes nothing. Side is
// the side of the target's screen the cursor enters from ("left", "right",
// "up", "down", or "" when it was taken back without crossing an edge). Y is
// the cursor's position along that edge in MWB's 0-65535 space: its height on a
// left or right edge, its horizontal offset on an up or down (top/bottom) edge.
type CursorAt struct {
	Epoch  uint32
	Target uint32
	Side   string
	Y      int32
}

const cursorAtVersion = 1

// NameHash identifies a machine by name in a LinuxCursor packet; names are
// unique in a group and up to 15 bytes, more than the payload has room for.
func NameHash(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()
}

// NewCursorPacket builds a LinuxCursor packet.
func NewCursorPacket(src, des uint32, c CursorAt) *Packet {
	p := &Packet{Type: LinuxCursor, Src: src, Des: des}
	var side int32
	switch c.Side {
	case "left":
		side = 1
	case "right":
		side = 2
	case "up":
		side = 3
	case "down":
		side = 4
	}
	p.Mouse = MouseData{
		X:          int32(c.Epoch),
		Y:          int32(c.Target),
		WheelDelta: max(0, min(65535, c.Y)),
		DwFlags:    cursorAtVersion | side<<8,
	}
	return p
}

// CursorAt decodes a LinuxCursor packet.
func (p *Packet) CursorAt() (c CursorAt, ok bool) {
	if p.Type != LinuxCursor || p.Mouse.DwFlags&0xff != cursorAtVersion {
		return CursorAt{}, false
	}
	c.Epoch, c.Target = uint32(p.Mouse.X), uint32(p.Mouse.Y)
	c.Y = max(0, min(65535, p.Mouse.WheelDelta))
	switch p.Mouse.DwFlags >> 8 & 0xff {
	case 1:
		c.Side = "left"
	case 2:
		c.Side = "right"
	case 3:
		c.Side = "up"
	case 4:
		c.Side = "down"
	}
	return c, true
}
