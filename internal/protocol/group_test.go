// internal/protocol/group_test.go
package protocol

import (
	"bytes"
	"strings"
	"testing"
)

func TestGroupChunksRoundTripOverTheWire(t *testing.T) {
	for _, size := range []int{1, GroupChunkData, GroupChunkData + 1, 700, MaxGroupDocument} {
		doc := []byte(strings.Repeat("abcdefghij", size/10+1)[:size])
		pkts, err := GroupChunks(3, 4, 9, doc)
		if err != nil {
			t.Fatal(err)
		}
		var a GroupAssembler
		var got []byte
		for i, p := range pkts {
			wire := p.Marshal()
			if len(wire) != PacketSize {
				t.Fatalf("chunk is %d bytes, want a plain %d-byte packet", len(wire), PacketSize)
			}
			back, err := UnmarshalPacket(wire)
			if err != nil {
				t.Fatal(err)
			}
			doc, done := a.Add(back)
			if done != (i == len(pkts)-1) {
				t.Fatalf("size %d: done=%v at chunk %d of %d", size, done, i, len(pkts))
			}
			got = doc
		}
		if !bytes.Equal(got, doc) {
			t.Fatalf("size %d: document changed in transit", size)
		}
	}
	if _, err := GroupChunks(1, 2, 0, make([]byte, MaxGroupDocument+1)); err == nil {
		t.Fatal("an oversized document was split")
	}
}

func TestGroupAssemblerDropsBrokenSequences(t *testing.T) {
	first, _ := GroupChunks(1, 2, 1, bytes.Repeat([]byte("x"), 30))
	second, _ := GroupChunks(1, 2, 2, []byte(`{"v":1}`))
	var a GroupAssembler
	a.Add(first[0])
	// A new document starts before the first finished: only the new one counts.
	if doc, done := a.Add(second[0]); !done || string(doc) != `{"v":1}` {
		t.Fatalf("got %q done=%v", doc, done)
	}
	// A missing chunk drops the document instead of returning a gap.
	a.Add(first[0])
	if _, done := a.Add(first[2]); done {
		t.Fatal("a document with a missing chunk was returned")
	}
}

func TestCursorAtRoundTrip(t *testing.T) {
	for _, in := range []CursorAt{
		{Epoch: 7, Target: NameHash("desk"), Side: "left", Y: 30000},
		{Epoch: 1 << 31, Target: NameHash("laptop"), Side: "right", Y: 0},
		{Epoch: 3, Target: NameHash("x"), Side: "", Y: 65535},
		// Up and down carry a horizontal offset in the same field.
		{Epoch: 9, Target: NameHash("top"), Side: "up", Y: 12345},
		{Epoch: 11, Target: NameHash("bottom"), Side: "down", Y: 54321},
	} {
		back, err := UnmarshalPacket(NewCursorPacket(1, 2, in).Marshal())
		if err != nil {
			t.Fatal(err)
		}
		out, ok := back.CursorAt()
		if !ok || out != in {
			t.Fatalf("sent %+v, got %+v ok=%v", in, out, ok)
		}
	}
	if _, ok := (&Packet{Type: Mouse}).CursorAt(); ok {
		t.Fatal("a Mouse packet decoded as CursorAt")
	}
	if NameHash("a") == NameHash("b") {
		t.Fatal("distinct names hash the same")
	}
}

func TestGroupPacketsArePlain(t *testing.T) {
	for _, pt := range []PackageType{LinuxGroup, LinuxCursor} {
		if pt&Matrix == Matrix || IsBigPacket(pt) {
			t.Fatalf("type %d must be a plain 32-byte packet", pt)
		}
	}
}
