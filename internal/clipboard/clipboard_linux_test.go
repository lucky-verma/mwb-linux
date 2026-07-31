//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
	"testing"
)

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
