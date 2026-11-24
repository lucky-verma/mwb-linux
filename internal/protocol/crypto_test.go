// internal/protocol/crypto_test.go
package protocol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestDeriveKey(t *testing.T) {
	key1 := DeriveKey("TestSecurityKey!!")
	key2 := DeriveKey("TestSecurityKey!!")
	if len(key1) != 32 {
		t.Fatalf("key length = %d, want 32", len(key1))
	}
	for i := range key1 {
		if key1[i] != key2[i] {
			t.Fatal("same input should produce same key")
		}
	}
	key3 := DeriveKey("DifferentKey12345")
	same := true
	for i := range key1 {
		if key1[i] != key3[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different input should produce different key")
	}
}

func TestFixedIV(t *testing.T) {
	iv := FixedIV()
	if len(iv) != 16 {
		t.Fatalf("iv length = %d, want 16", len(iv))
	}
	expected := []byte("1844674407370955")
	for i := range iv {
		if iv[i] != expected[i] {
			t.Errorf("iv[%d] = %d, want %d", i, iv[i], expected[i])
		}
	}
}

func TestGet24BitHash(t *testing.T) {
	h1 := Get24BitHash("TestSecurityKey!!")
	h2 := Get24BitHash("TestSecurityKey!!")
	if h1 != h2 {
		t.Errorf("same input should produce same hash: %d != %d", h1, h2)
	}
	h3 := Get24BitHash("DifferentKey12345")
	if h1 == h3 {
		t.Error("different input produced same hash (unlikely collision)")
	}
	if Get24BitHash("") != 0 {
		t.Error("empty string should return 0")
	}
}

func TestPBKDF2SaltIsUTF16LE(t *testing.T) {
	salt := pbkdf2Salt()
	if len(salt) != 40 {
		t.Fatalf("salt length = %d, want 40", len(salt))
	}
	if salt[0] != 0x31 || salt[1] != 0x00 {
		t.Errorf("salt[0:2] = [%x, %x], want [31, 00]", salt[0], salt[1])
	}
	if salt[2] != 0x38 || salt[3] != 0x00 {
		t.Errorf("salt[2:4] = [%x, %x], want [38, 00]", salt[2], salt[3])
	}
}

func TestChecksumAndMagic(t *testing.T) {
	magic := Get24BitHash("TestSecurityKey!!")

	p := &Packet{Type: Mouse, ID: 1, Src: 1, Des: 2}
	p.Mouse.X = 100
	p.Mouse.Y = 200
	buf := p.Marshal()

	StampPacket(buf, magic)

	expectedHi := byte((magic >> 24) & 0xFF)
	expectedLo := byte((magic >> 16) & 0xFF)
	if buf[3] != expectedHi || buf[2] != expectedLo {
		t.Errorf("magic bytes = [%x, %x], want [%x, %x]", buf[2], buf[3], expectedLo, expectedHi)
	}

	if err := ValidatePacket(buf, magic); err != nil {
		t.Errorf("validation failed: %v", err)
	}

	buf[15] ^= 0xFF
	if err := ValidatePacket(buf, magic); err == nil {
		t.Error("validation should fail after corruption")
	}
}

// TestValidatePacketMagicMismatchIsSentinel keeps the wrong-key case
// identifiable: the network layer matches ErrMagicMismatch to tell the user
// which setting to check (issue #53), so the message alone is not enough.
func TestValidatePacketMagicMismatchIsSentinel(t *testing.T) {
	p := &Packet{Type: Handshake, ID: 1, Src: 1, Des: 2}
	buf := p.Marshal()
	StampPacket(buf, Get24BitHash("KeyOnWindows1234"))

	err := ValidatePacket(buf, Get24BitHash("KeyOnLinux567890"))
	if !errors.Is(err, ErrMagicMismatch) {
		t.Fatalf("ValidatePacket with a different key = %v, want ErrMagicMismatch", err)
	}
	if !strings.HasPrefix(err.Error(), "magic mismatch: wire=0x") {
		t.Errorf("error text changed to %q; users search for the old wording", err)
	}
}

func TestDeriveKeyWithSalt(t *testing.T) {
	salt1 := bytes.Repeat([]byte{0x01}, SaltSize)
	salt2 := bytes.Repeat([]byte{0x02}, SaltSize)

	key1 := DeriveKeyWithSalt("TestSecurityKey!!", salt1)
	if len(key1) != aesKeySize {
		t.Fatalf("key length = %d, want %d", len(key1), aesKeySize)
	}

	// Same key and salt must reproduce the same bytes.
	if !bytes.Equal(key1, DeriveKeyWithSalt("TestSecurityKey!!", salt1)) {
		t.Error("derivation is not deterministic for a fixed key and salt")
	}

	// A different salt must produce a different key — that is the whole point
	// of PowerToys sending a fresh salt per connection.
	if bytes.Equal(key1, DeriveKeyWithSalt("TestSecurityKey!!", salt2)) {
		t.Error("different salts produced the same key")
	}

	// So must a different security key.
	if bytes.Equal(key1, DeriveKeyWithSalt("DifferentKey12345", salt1)) {
		t.Error("different security keys produced the same key")
	}
}

// TestDeriveKeyKnownAnswers pins both PBKDF2 schemes to fixed outputs, so a
// change of implementation cannot silently change the keys PowerToys expects.
// The expected values were cross-checked with Python's hashlib.pbkdf2_hmac.
func TestDeriveKeyKnownAnswers(t *testing.T) {
	salt := make([]byte, SaltSize)
	for i := range salt {
		salt[i] = byte(i)
	}
	tests := []struct {
		name string
		got  []byte
		want string
	}{
		{"legacy fixed salt, 50k", DeriveKey("TestSecurityKey!!"),
			"92b7fc2b9686a3a0243a3e99b13c51ca84c4e907bc9e221bb5a7bfd353f6f2f3"},
		{"per-connection salt, 100k", DeriveKeyWithSalt("TestSecurityKey!!", salt),
			"5fca849920ece2fda0ff99537f580508fae95b9466c9bb4e52984093623984bc"},
	}
	for _, tt := range tests {
		if got := hex.EncodeToString(tt.got); got != tt.want {
			t.Errorf("%s: key = %s, want %s", tt.name, got, tt.want)
		}
	}
}
