//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

func controlPair(t *testing.T) (*network.Conn, *network.Conn) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	stop := make(chan struct{})
	incoming, err := network.ListenAndAccept(port, "TestSecurityKey!!", "windows", "127.0.0.1", nil, stop)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { close(stop) })

	client, err := network.Connect(fmt.Sprintf("127.0.0.1:%d", port), "TestSecurityKey!!", "linux", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	var server *network.Conn
	select {
	case server = <-incoming:
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not return the control connection")
	}
	t.Cleanup(func() { _ = server.Close() })

	return client, server
}

// Official PowerToys sends Clipboard (a beat) for files and oversized data,
// remembers it, and waits until the cursor switches to the receiving machine
// before sending ClipboardAsk. Asking on the beat itself downloads every file
// the user merely copies on Windows.
func TestClipboardBeatWaitsForActivationBeforeRequest(t *testing.T) {
	client, server := controlPair(t)
	m := NewManager(client, "")

	m.HandlePacket(&protocol.Packet{Type: protocol.Clipboard, Src: client.RemoteID})
	m.mu.Lock()
	pending := m.pendingPull
	m.mu.Unlock()
	if pending != client.RemoteID {
		t.Fatalf("pending remote = %d, want %d", pending, client.RemoteID)
	}

	// MachineSwitched is the point where official MWB retrieves the pending
	// large/file clipboard and prepares it for a later Ctrl+V.
	m.HandleActivation()
	var got *protocol.Packet
	for range 32 {
		pkt, err := server.RecvPacket()
		if err != nil {
			t.Fatal(err)
		}
		if pkt.Type == protocol.ClipboardAsk {
			got = pkt
			break
		}
	}
	if got == nil {
		t.Fatal("ClipboardAsk was not sent after the remote beat")
		return
	}
	if got.Type != protocol.ClipboardAsk {
		t.Fatalf("Type = %d, want ClipboardAsk", got.Type)
	}
	if got.Src != client.MachineID || got.Des != client.RemoteID {
		t.Errorf("Src/Des = %d/%d, want %d/%d", got.Src, got.Des, client.MachineID, client.RemoteID)
	}
	if got.MachineName() != "linux" {
		t.Errorf("MachineName = %q, want linux", got.MachineName())
	}
	if got.PostAction != protocol.PostActionOther {
		t.Errorf("PostAction = %d, want Other", got.PostAction)
	}
}

func TestDeflateDecompressRoundTrip(t *testing.T) {
	want := []byte("clipboard payload")
	compressed, err := deflateCompress(want)
	if err != nil {
		t.Fatal(err)
	}

	got, err := deflateDecompress(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decompressed payload = %q, want %q", got, want)
	}
}

func TestDeflateDecompressRejectsOversizedOutput(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 1025)
	compressed, err := deflateCompress(payload)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := deflateDecompressLimit(compressed, 1024); !errors.Is(err, errDecompressedTooLarge) {
		t.Fatalf("error = %v, want %v", err, errDecompressedTooLarge)
	}
}

// --- inbound chunk reassembly bounds ---

// A peer streams clipboard content as chunks with no declared total, so this
// bound is the only thing stopping it filling memory. It had no test.
//
// Overflow drops the stream and resets, after which the next chunk starts a new
// one. That means a peer can stream indefinitely, but never holds more than the
// limit at once, which is the property that matters.
func TestHandleChunk_NeverExceedsReceiveLimit(t *testing.T) {
	m := &Manager{}
	chunk := make([]byte, dataSize)

	peak := 0
	for i := 0; i < (maxRecvBuf/dataSize)*3; i++ {
		m.handleChunk(&protocol.Packet{Type: protocol.ClipboardText, ClipboardData: chunk})
		m.mu.Lock()
		if n := m.recvBuf.Len(); n > peak {
			peak = n
		}
		m.mu.Unlock()
	}

	if peak > maxRecvBuf {
		t.Errorf("buffer peaked at %d bytes, limit is %d; a peer can exhaust memory", peak, maxRecvBuf)
	}
	if peak == 0 {
		t.Error("nothing buffered at all; the test is not exercising the path")
	}
}

// The moment the limit is crossed the stream must be dropped and the buffer
// released, rather than trimmed and carried forward.
func TestHandleChunk_DropsStreamOnOverflow(t *testing.T) {
	m := &Manager{}
	chunk := make([]byte, dataSize)

	for i := 0; i < (maxRecvBuf/dataSize)+1; i++ {
		m.handleChunk(&protocol.Packet{Type: protocol.ClipboardText, ClipboardData: chunk})
	}

	m.mu.Lock()
	buffered := m.recvBuf.Len()
	receiving := m.receiving
	m.mu.Unlock()

	if receiving {
		t.Error("stream still marked as receiving after the limit was crossed")
	}
	if buffered != 0 {
		t.Errorf("buffer holds %d bytes after overflow, want it released", buffered)
	}
}

// An image stream must be tagged on its first chunk, or the completed payload
// is handed to the text path.
func TestHandleChunk_TracksImageStreams(t *testing.T) {
	m := &Manager{}
	m.handleChunk(&protocol.Packet{Type: protocol.ClipboardImage, ClipboardData: []byte("PNG")})

	m.mu.Lock()
	isImage := m.recvIsImage
	m.mu.Unlock()
	if !isImage {
		t.Error("image stream not tagged; the payload would be treated as text")
	}
}
