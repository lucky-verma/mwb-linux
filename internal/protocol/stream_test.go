// internal/protocol/stream_test.go
package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key := DeriveKey("TestSecurityKey!!")
	iv := FixedIV()

	plaintext := []byte("Hello, Mouse Without Borders!!!!") // exactly 32 bytes
	if len(plaintext) != 32 {
		t.Fatalf("plaintext len = %d, want 32", len(plaintext))
	}

	// Encrypt
	var cipherBuf bytes.Buffer
	enc, err := NewEncryptWriter(&cipherBuf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enc.Write(plaintext); err != nil {
		t.Fatal(err)
	}

	// Ciphertext should not equal plaintext
	cipher := cipherBuf.Bytes()
	if bytes.Equal(cipher, plaintext) {
		t.Error("ciphertext should differ from plaintext")
	}

	// Decrypt
	dec, err := NewDecryptReader(bytes.NewReader(cipher), key, iv)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, len(plaintext))
	if _, err := io.ReadFull(dec, result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result, plaintext) {
		t.Errorf("decrypted = %q, want %q", result, plaintext)
	}
}

func TestEncryptedStreamMultipleBlocks(t *testing.T) {
	key := DeriveKey("AnotherTestKey123")
	iv := FixedIV()

	// Write two 32-byte packets
	pkt1 := bytes.Repeat([]byte{0xAA}, 32)
	pkt2 := bytes.Repeat([]byte{0xBB}, 32)

	var cipherBuf bytes.Buffer
	enc, err := NewEncryptWriter(&cipherBuf, key, iv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enc.Write(pkt1); err != nil {
		t.Fatal(err)
	}
	if _, err = enc.Write(pkt2); err != nil {
		t.Fatal(err)
	}

	dec, err := NewDecryptReader(bytes.NewReader(cipherBuf.Bytes()), key, iv)
	if err != nil {
		t.Fatal(err)
	}

	got1 := make([]byte, 32)
	if _, err = io.ReadFull(dec, got1); err != nil {
		t.Fatal(err)
	}
	got2 := make([]byte, 32)
	if _, err = io.ReadFull(dec, got2); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got1, pkt1) {
		t.Error("packet 1 mismatch")
	}
	if !bytes.Equal(got2, pkt2) {
		t.Error("packet 2 mismatch")
	}
}

// TestHeaderStreamRoundTrip covers the wire format PowerToys has used since it
// moved to a per-connection salt and IV: 32 cleartext bytes, then ciphertext.
func TestHeaderStreamRoundTrip(t *testing.T) {
	const securityKey = "TestSecurityKey!!"
	plaintext := bytes.Repeat([]byte("mwb protocol data"), 4)[:64]

	var wire bytes.Buffer
	enc, err := NewEncryptWriterWithHeader(&wire, securityKey)
	if err != nil {
		t.Fatalf("NewEncryptWriterWithHeader: %v", err)
	}
	if _, err := enc.Write(plaintext); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The header must be on the wire ahead of the ciphertext, in the clear.
	if got, want := wire.Len(), HeaderSize+len(plaintext); got != want {
		t.Fatalf("wire length = %d, want %d", got, want)
	}
	if bytes.Equal(wire.Bytes()[HeaderSize:], plaintext) {
		t.Error("payload was written in the clear")
	}

	dec, err := NewDecryptReaderWithHeader(bytes.NewReader(wire.Bytes()), securityKey)
	if err != nil {
		t.Fatalf("NewDecryptReaderWithHeader: %v", err)
	}
	got := make([]byte, len(plaintext))
	if _, err := io.ReadFull(dec, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("round trip = %q, want %q", got, plaintext)
	}
}

// A reader keyed with the wrong security key must not recover the plaintext,
// even though it parses the same cleartext header.
func TestHeaderStreamWrongKey(t *testing.T) {
	var wire bytes.Buffer
	enc, err := NewEncryptWriterWithHeader(&wire, "TestSecurityKey!!")
	if err != nil {
		t.Fatalf("NewEncryptWriterWithHeader: %v", err)
	}
	plaintext := bytes.Repeat([]byte{0xAB}, 32)
	if _, err := enc.Write(plaintext); err != nil {
		t.Fatalf("write: %v", err)
	}

	dec, err := NewDecryptReaderWithHeader(bytes.NewReader(wire.Bytes()), "WrongSecurityKey!")
	if err != nil {
		t.Fatalf("NewDecryptReaderWithHeader: %v", err)
	}
	got := make([]byte, len(plaintext))
	if _, err := io.ReadFull(dec, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if bytes.Equal(got, plaintext) {
		t.Error("wrong key decrypted the payload")
	}
}
