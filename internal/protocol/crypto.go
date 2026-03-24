// internal/protocol/crypto.go
package protocol

import (
	"crypto/sha512"
	"encoding/binary"
	"fmt"

	"golang.org/x/crypto/pbkdf2"
)

const (
	aesKeySize   = 32
	aesBlockSize = 16
	pbkdf2Iter   = 50000
	initialIVStr = "18446744073709551615"
)

// pbkdf2Salt returns the UTF-16LE encoding of "18446744073709551615" (40 bytes).
func pbkdf2Salt() []byte {
	s := initialIVStr
	salt := make([]byte, len(s)*2)
	for i, c := range s {
		binary.LittleEndian.PutUint16(salt[i*2:], uint16(c))
	}
	return salt
}

// DeriveKey derives a 32-byte AES-256 key using PBKDF2-SHA512 with 50,000 iterations.
func DeriveKey(securityKey string) []byte {
	return pbkdf2.Key([]byte(securityKey), pbkdf2Salt(), pbkdf2Iter, aesKeySize, sha512.New)
}

// FixedIV returns the 16-byte IV for AES-CBC (ASCII "1844674407370955").
func FixedIV() []byte {
	s := initialIVStr
	if len(s) > aesBlockSize {
		s = s[:aesBlockSize]
	}
	return []byte(s)
}

// Get24BitHash computes the MWB magic number from the security key.
func Get24BitHash(key string) uint32 {
	if key == "" {
		return 0
	}
	buf := make([]byte, PacketSize)
	for i := 0; i < len(key) && i < PacketSize; i++ {
		buf[i] = byte(key[i])
	}
	h := sha512.New()
	h.Write(buf)
	hash := h.Sum(nil)
	for i := 0; i < 50000; i++ {
		h.Reset()
		h.Write(hash)
		hash = h.Sum(nil)
	}
	return uint32((int(hash[0]) << 23) + (int(hash[1]) << 16) + (int(hash[len(hash)-1]) << 8) + int(hash[2]))
}

// StampPacket sets the magic number and checksum in a marshaled packet buffer.
func StampPacket(buf []byte, magic uint32) {
	buf[3] = byte((magic >> 24) & 0xFF)
	buf[2] = byte((magic >> 16) & 0xFF)
	buf[1] = 0
	for i := 2; i < PacketSize; i++ {
		buf[1] = buf[1] + buf[i]
	}
}

// ValidatePacket checks the magic number and checksum.
func ValidatePacket(buf []byte, magic uint32) error {
	wireMagic := uint32(buf[3])<<24 + uint32(buf[2])<<16
	expectedMagic := magic & 0xFFFF0000
	if wireMagic != expectedMagic {
		return fmt.Errorf("magic mismatch: wire=0x%08X expected=0x%08X", wireMagic, expectedMagic)
	}
	var checksum byte
	for i := 2; i < PacketSize; i++ {
		checksum += buf[i]
	}
	if buf[1] != checksum {
		return fmt.Errorf("checksum mismatch: wire=0x%02X computed=0x%02X", buf[1], checksum)
	}
	return nil
}

// ClearStamp zeroes bytes 1-3 after validation.
func ClearStamp(buf []byte) {
	buf[1] = 0
	buf[2] = 0
	buf[3] = 0
}
