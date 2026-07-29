//go:build linux

package clipboard

import (
	"bytes"
	"errors"
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
