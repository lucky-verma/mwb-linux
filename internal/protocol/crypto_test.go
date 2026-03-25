// internal/protocol/crypto_test.go
package protocol

import (
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
