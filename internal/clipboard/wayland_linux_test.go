//go:build linux

package clipboard

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// waylandEvent builds one wire message: object, opcode, then arguments.
func waylandEvent(obj, op uint32, args []byte) []byte {
	b := binary.LittleEndian.AppendUint32(nil, obj)
	b = binary.LittleEndian.AppendUint32(b, uint32(8+len(args))<<16|op)
	return append(b, args...)
}

func global(name uint32, iface string, version uint32) []byte {
	b := binary.LittleEndian.AppendUint32(nil, name)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(iface)+1))
	b = append(b, iface...)
	b = append(b, 0)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return binary.LittleEndian.AppendUint32(b, version)
}

func TestReadGlobalsStopsAtTheSyncCallback(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(waylandEvent(2, 0, global(1, "wl_compositor", 6)))
	stream.Write(waylandEvent(2, 0, global(7, "zwlr_data_control_manager_v1", 2)))
	stream.Write(waylandEvent(3, 0, binary.LittleEndian.AppendUint32(nil, 99))) // done
	stream.Write(waylandEvent(2, 0, global(9, "after_sync", 1)))
	got, err := readGlobals(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "wl_compositor" || got[1] != "zwlr_data_control_manager_v1" {
		t.Fatalf("globals %v", got)
	}
	if _, err := readGlobals(bytes.NewReader(waylandEvent(1, 0, make([]byte, 12)))); err == nil {
		t.Fatal("a compositor error was not reported")
	}
	if _, err := readGlobals(bytes.NewReader([]byte{2, 0, 0, 0, 0, 0, 2, 0})); err == nil {
		t.Fatal("a malformed size was accepted")
	}
}
