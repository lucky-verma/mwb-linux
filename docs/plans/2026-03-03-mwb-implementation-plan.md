# MWB Linux Client Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go application that speaks the MWB wire protocol, receiving mouse/keyboard input from a Windows host and injecting it via uinput.

**Architecture:** TCP client/server connecting to Windows MWB on port BASE_PORT+1 (default 15101). AES-256-CBC encrypted streams with PBKDF2-SHA512 key derivation. Fixed-size binary packets (32/64 bytes). Input injection via Linux uinput kernel interface.

**Tech Stack:** Go, standard library crypto, golang.org/x/crypto (PBKDF2), BurntSushi/toml, direct uinput ioctl syscalls (no CGo).

**Key protocol corrections from source code analysis:**
- Message port = BASE_PORT + 1 (15101), clipboard port = BASE_PORT (15100)
- PBKDF2 salt is UTF-16LE encoding of "18446744073709551615" (40 bytes, not UTF-8)
- Mouse dwFlags uses WM_* message constants (0x200=MOUSEMOVE, 0x201=LBUTTONDOWN, etc.), not MOUSEEVENTF_*
- Keyboard dwFlags uses LLKHF flags (0x80=UP, 0x01=EXTENDED)
- MOUSEDATA starts at packet offset 16 (not 12): X@16, Y@20, WheelDelta@24, dwFlags@28
- Machine names are ASCII, space-padded to 32 bytes, split across 4 int64 fields at offsets 32-63
- Both sides send 10 Handshake packets; HandshakeAck proves shared key by bitwise-inverting Machine1-4 fields

---

### Task 1: Project Scaffold

**Files:**
- Create: `go.mod`
- Create: `cmd/mwb/main.go`
- Create: `internal/protocol/types.go`

**Step 1: Initialize Go module**

Run: `cd /home/bjk/projects/scratch/mwb && go mod init github.com/bjk/mwb`

**Step 2: Create directory structure**

Run:
```bash
mkdir -p cmd/mwb internal/protocol internal/network internal/input internal/config
```

**Step 3: Write minimal main.go**

```go
// cmd/mwb/main.go
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "mwb: Mouse Without Borders Linux client")
	os.Exit(0)
}
```

**Step 4: Verify it compiles and runs**

Run: `go build -o mwb ./cmd/mwb && ./mwb`
Expected: prints "mwb: Mouse Without Borders Linux client"

**Step 5: Commit**

```bash
git init && git add -A && git commit -m "feat: initial project scaffold"
```

---

### Task 2: Protocol Types and Constants

**Files:**
- Create: `internal/protocol/types.go`
- Create: `internal/protocol/types_test.go`

**Step 1: Write the test**

```go
// internal/protocol/types_test.go
package protocol

import "testing"

func TestPackageTypeValues(t *testing.T) {
	tests := []struct {
		name string
		pt   PackageType
		want byte
	}{
		{"Mouse", Mouse, 123},
		{"Keyboard", Keyboard, 122},
		{"Handshake", Handshake, 126},
		{"HandshakeAck", HandshakeAck, 127},
		{"Heartbeat", Heartbeat, 20},
		{"ByeBye", ByeBye, 4},
		{"Invalid", Invalid, 0xFF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if byte(tt.pt) != tt.want {
				t.Errorf("got %d, want %d", byte(tt.pt), tt.want)
			}
		})
	}
}

func TestIsBigPacket(t *testing.T) {
	big := []PackageType{Hello, Awake, Heartbeat, HeartbeatEx, Handshake, HandshakeAck}
	for _, pt := range big {
		if !IsBigPacket(pt) {
			t.Errorf("%d should be big packet", pt)
		}
	}
	small := []PackageType{Mouse, Keyboard, ByeBye, HideMouse}
	for _, pt := range small {
		if IsBigPacket(pt) {
			t.Errorf("%d should be small packet", pt)
		}
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /home/bjk/projects/scratch/mwb && go test ./internal/protocol/ -v -run TestPackageType`
Expected: FAIL (types not defined)

**Step 3: Write implementation**

```go
// internal/protocol/types.go
package protocol

// PackageType identifies the type of MWB packet.
type PackageType byte

const (
	Hi                       PackageType = 2
	Hello                    PackageType = 3
	ByeBye                   PackageType = 4
	Heartbeat                PackageType = 20
	Awake                    PackageType = 21
	HideMouse                PackageType = 50
	HeartbeatEx              PackageType = 51
	HeartbeatExL2            PackageType = 52
	HeartbeatExL3            PackageType = 53
	Clipboard                PackageType = 69
	ClipboardDragDrop        PackageType = 70
	ClipboardDragDropEnd     PackageType = 71
	ExplorerDragDrop         PackageType = 72
	ClipboardCapture         PackageType = 73
	CaptureScreenCommand     PackageType = 74
	ClipboardDragDropOp      PackageType = 75
	ClipboardDataEnd         PackageType = 76
	MachineSwitched          PackageType = 77
	ClipboardAsk             PackageType = 78
	ClipboardPush            PackageType = 79
	NextMachine              PackageType = 121
	Keyboard                 PackageType = 122
	Mouse                    PackageType = 123
	ClipboardText            PackageType = 124
	ClipboardImage           PackageType = 125
	Handshake                PackageType = 126
	HandshakeAck             PackageType = 127
	Matrix                   PackageType = 128
	Invalid                  PackageType = 0xFF
	Error                    PackageType = 0xFE
)

const (
	PacketSize   = 32
	PacketSizeEx = 64
)

// WM_* mouse message constants used as MOUSEDATA.dwFlags on the wire.
const (
	WM_MOUSEMOVE    = 0x0200
	WM_LBUTTONDOWN  = 0x0201
	WM_LBUTTONUP    = 0x0202
	WM_RBUTTONDOWN  = 0x0204
	WM_RBUTTONUP    = 0x0205
	WM_MBUTTONDOWN  = 0x0207
	WM_MBUTTONUP    = 0x0208
	WM_MOUSEWHEEL   = 0x020A
	WM_XBUTTONDOWN  = 0x020B
	WM_XBUTTONUP    = 0x020C
	WM_MOUSEHWHEEL  = 0x020E
)

// LLKHF keyboard flag bits used as KEYBDDATA.dwFlags on the wire.
const (
	LLKHF_EXTENDED = 0x01
	LLKHF_UP       = 0x80
)

// MoveMouseRelative is the offset added to X/Y to signal relative mouse movement.
const MoveMouseRelative = 100000

// IsBigPacket returns true if the packet type uses 64-byte extended format.
func IsBigPacket(pt PackageType) bool {
	switch pt {
	case Hello, Awake, Heartbeat, HeartbeatEx,
		Handshake, HandshakeAck,
		ClipboardPush, Clipboard, ClipboardAsk,
		ClipboardImage, ClipboardText, ClipboardDataEnd:
		return true
	}
	// Matrix types have bit 128 set
	if pt&Matrix == Matrix {
		return true
	}
	return false
}
```

**Step 4: Run tests**

Run: `go test ./internal/protocol/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/protocol/types.go internal/protocol/types_test.go
git commit -m "feat: protocol types and constants"
```

---

### Task 3: Packet Structs and Binary Serialization

**Files:**
- Create: `internal/protocol/packet.go`
- Create: `internal/protocol/packet_test.go`

**Step 1: Write the test**

```go
// internal/protocol/packet_test.go
package protocol

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestPacketMarshalRoundTrip(t *testing.T) {
	p := &Packet{
		Type: Mouse,
		ID:   42,
		Src:  1,
		Des:  2,
	}
	p.Mouse.X = 32768
	p.Mouse.Y = 16384
	p.Mouse.WheelDelta = 0
	p.Mouse.DwFlags = WM_MOUSEMOVE

	buf := p.Marshal()
	if len(buf) != PacketSize {
		t.Fatalf("marshal len = %d, want %d", len(buf), PacketSize)
	}

	p2, err := UnmarshalPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Type != Mouse {
		t.Errorf("type = %d, want %d", p2.Type, Mouse)
	}
	if p2.ID != 42 {
		t.Errorf("id = %d, want 42", p2.ID)
	}
	if p2.Mouse.X != 32768 || p2.Mouse.Y != 16384 {
		t.Errorf("mouse = (%d,%d), want (32768,16384)", p2.Mouse.X, p2.Mouse.Y)
	}
}

func TestPacketMarshalBigPacket(t *testing.T) {
	p := &Packet{
		Type: Handshake,
		ID:   1,
		Src:  1,
		Des:  0,
	}
	p.SetMachineName("mylinuxbox")
	p.Handshake.Machine1 = 0xDEADBEEF
	p.Handshake.Machine2 = 0xCAFEBABE
	p.Handshake.Machine3 = 0x12345678
	p.Handshake.Machine4 = 0xABCD0123

	buf := p.Marshal()
	if len(buf) != PacketSizeEx {
		t.Fatalf("marshal len = %d, want %d", len(buf), PacketSizeEx)
	}

	p2, err := UnmarshalPacket(buf)
	if err != nil {
		t.Fatal(err)
	}
	name := p2.MachineName()
	if name != "mylinuxbox" {
		t.Errorf("machine name = %q, want %q", name, "mylinuxbox")
	}
	if p2.Handshake.Machine1 != 0xDEADBEEF {
		t.Errorf("machine1 = 0x%X, want 0xDEADBEEF", p2.Handshake.Machine1)
	}
}

func TestMachineNamePadding(t *testing.T) {
	p := &Packet{Type: Handshake}
	p.SetMachineName("ab")
	buf := p.Marshal()
	// Machine name is at bytes 32-63, ASCII space-padded to 32 bytes
	nameBytes := buf[32:64]
	if nameBytes[0] != 'a' || nameBytes[1] != 'b' || nameBytes[2] != ' ' {
		t.Errorf("name bytes = %v, want 'ab' + spaces", nameBytes[:4])
	}

	p2, _ := UnmarshalPacket(buf)
	if p2.MachineName() != "ab" {
		t.Errorf("got %q, want %q", p2.MachineName(), "ab")
	}
}

func TestPacketFieldOffsets(t *testing.T) {
	// Verify exact byte layout matches MWB C# StructLayout
	p := &Packet{Type: Keyboard, ID: 0, Src: 0, Des: 0}
	p.Keyboard.DateTime = 0x0102030405060708
	p.Keyboard.WVk = 0x41 // VK_A
	p.Keyboard.DwFlags = 0

	buf := p.Marshal()
	// DateTime at offset 16 (8 bytes, little-endian)
	dt := binary.LittleEndian.Uint64(buf[16:24])
	if dt != 0x0102030405060708 {
		t.Errorf("datetime at offset 16 = 0x%X, want 0x0102030405060708", dt)
	}
	// wVk at offset 24 (4 bytes)
	vk := binary.LittleEndian.Uint32(buf[24:28])
	if vk != 0x41 {
		t.Errorf("wVk at offset 24 = 0x%X, want 0x41", vk)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/protocol/ -v -run TestPacket`
Expected: FAIL

**Step 3: Write implementation**

```go
// internal/protocol/packet.go
package protocol

import (
	"encoding/binary"
	"fmt"
	"strings"
)

// MouseData matches MWB MOUSEDATA struct at packet offset 16.
type MouseData struct {
	X          int32
	Y          int32
	WheelDelta int32
	DwFlags    int32
}

// KeyboardData matches MWB KEYBDDATA struct.
// DateTime at offset 16, wVk at offset 24, dwFlags at offset 28.
type KeyboardData struct {
	DateTime int64
	WVk      int32
	DwFlags  int32
}

// HandshakeData holds Machine1-4 at offsets 16-28.
type HandshakeData struct {
	Machine1 uint32
	Machine2 uint32
	Machine3 uint32
	Machine4 uint32
}

// Packet represents a decoded MWB packet.
// The payload union is represented by separate fields; only one is active
// depending on Type.
type Packet struct {
	Type PackageType
	ID   int32
	Src  uint32
	Des  uint32

	// Payload union (only one active based on Type)
	Mouse     MouseData
	Keyboard  KeyboardData
	Handshake HandshakeData

	// Extended area (bytes 32-63, only for big packets)
	machineName [32]byte
}

// SetMachineName sets the machine name (ASCII, max 32 chars, space-padded).
func (p *Packet) SetMachineName(name string) {
	for i := range p.machineName {
		p.machineName[i] = ' '
	}
	copy(p.machineName[:], []byte(name))
}

// MachineName returns the machine name, trimmed of spaces.
func (p *Packet) MachineName() string {
	return strings.TrimRight(string(p.machineName[:]), " ")
}

// Marshal serializes the packet to its wire format (32 or 64 bytes).
// Does NOT set checksum or magic number (that happens at send time).
func (p *Packet) Marshal() []byte {
	big := IsBigPacket(p.Type)
	size := PacketSize
	if big {
		size = PacketSizeEx
	}

	buf := make([]byte, size)

	// Header: bytes 0-15
	buf[0] = byte(p.Type)
	// bytes 1-3 reserved for checksum/magic (set at send time)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(p.ID))
	binary.LittleEndian.PutUint32(buf[8:12], p.Src)
	binary.LittleEndian.PutUint32(buf[12:16], p.Des)

	// Payload union: bytes 16-31
	switch p.Type {
	case Mouse:
		binary.LittleEndian.PutUint32(buf[16:20], uint32(p.Mouse.X))
		binary.LittleEndian.PutUint32(buf[20:24], uint32(p.Mouse.Y))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(p.Mouse.WheelDelta))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(p.Mouse.DwFlags))
	case Keyboard:
		binary.LittleEndian.PutUint64(buf[16:24], uint64(p.Keyboard.DateTime))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(p.Keyboard.WVk))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(p.Keyboard.DwFlags))
	case Handshake, HandshakeAck:
		binary.LittleEndian.PutUint32(buf[16:20], p.Handshake.Machine1)
		binary.LittleEndian.PutUint32(buf[20:24], p.Handshake.Machine2)
		binary.LittleEndian.PutUint32(buf[24:28], p.Handshake.Machine3)
		binary.LittleEndian.PutUint32(buf[28:32], p.Handshake.Machine4)
	}

	// Extended area: bytes 32-63 (machine name for big packets)
	if big {
		copy(buf[32:64], p.machineName[:])
	}

	return buf
}

// UnmarshalPacket decodes a raw packet buffer (32 or 64 bytes).
// Assumes bytes 1-3 have already been cleared after checksum/magic validation.
func UnmarshalPacket(buf []byte) (*Packet, error) {
	if len(buf) < PacketSize {
		return nil, fmt.Errorf("packet too short: %d bytes", len(buf))
	}

	p := &Packet{
		Type: PackageType(buf[0]),
		ID:   int32(binary.LittleEndian.Uint32(buf[4:8])),
		Src:  binary.LittleEndian.Uint32(buf[8:12]),
		Des:  binary.LittleEndian.Uint32(buf[12:16]),
	}

	switch p.Type {
	case Mouse:
		p.Mouse.X = int32(binary.LittleEndian.Uint32(buf[16:20]))
		p.Mouse.Y = int32(binary.LittleEndian.Uint32(buf[20:24]))
		p.Mouse.WheelDelta = int32(binary.LittleEndian.Uint32(buf[24:28]))
		p.Mouse.DwFlags = int32(binary.LittleEndian.Uint32(buf[28:32]))
	case Keyboard:
		p.Keyboard.DateTime = int64(binary.LittleEndian.Uint64(buf[16:24]))
		p.Keyboard.WVk = int32(binary.LittleEndian.Uint32(buf[24:28]))
		p.Keyboard.DwFlags = int32(binary.LittleEndian.Uint32(buf[28:32]))
	case Handshake, HandshakeAck:
		p.Handshake.Machine1 = binary.LittleEndian.Uint32(buf[16:20])
		p.Handshake.Machine2 = binary.LittleEndian.Uint32(buf[20:24])
		p.Handshake.Machine3 = binary.LittleEndian.Uint32(buf[24:28])
		p.Handshake.Machine4 = binary.LittleEndian.Uint32(buf[28:32])
	}

	// Extended area for big packets
	if IsBigPacket(p.Type) && len(buf) >= PacketSizeEx {
		copy(p.machineName[:], buf[32:64])
	}

	return p, nil
}
```

**Step 4: Run tests**

Run: `go test ./internal/protocol/ -v -run TestPacket`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/protocol/packet.go internal/protocol/packet_test.go
git commit -m "feat: packet structs and binary serialization"
```

---

### Task 4: Encryption - Key Derivation and Magic Number

**Files:**
- Create: `internal/protocol/crypto.go`
- Create: `internal/protocol/crypto_test.go`

**Step 1: Add golang.org/x/crypto dependency**

Run: `cd /home/bjk/projects/scratch/mwb && go get golang.org/x/crypto`

**Step 2: Write the test**

```go
// internal/protocol/crypto_test.go
package protocol

import (
	"testing"
)

func TestDeriveKey(t *testing.T) {
	// Verify key derivation produces 32 bytes deterministically
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

	// Different input -> different key
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
	// IV is ASCII "1844674407370955"
	expected := []byte("1844674407370955")
	for i := range iv {
		if iv[i] != expected[i] {
			t.Errorf("iv[%d] = %d, want %d", i, iv[i], expected[i])
		}
	}
}

func TestGet24BitHash(t *testing.T) {
	// Verify deterministic output
	h1 := Get24BitHash("TestSecurityKey!!")
	h2 := Get24BitHash("TestSecurityKey!!")
	if h1 != h2 {
		t.Errorf("same input should produce same hash: %d != %d", h1, h2)
	}

	// Different input -> different hash (with very high probability)
	h3 := Get24BitHash("DifferentKey12345")
	if h1 == h3 {
		t.Error("different input produced same hash (unlikely collision)")
	}

	// Empty string returns 0
	if Get24BitHash("") != 0 {
		t.Error("empty string should return 0")
	}
}

func TestPBKDF2SaltIsUTF16LE(t *testing.T) {
	// The salt must be UTF-16LE encoding of "18446744073709551615"
	// "1" in UTF-16LE is 0x31, 0x00
	salt := pbkdf2Salt()
	if len(salt) != 40 { // 20 chars * 2 bytes each
		t.Fatalf("salt length = %d, want 40", len(salt))
	}
	if salt[0] != 0x31 || salt[1] != 0x00 { // '1' in UTF-16LE
		t.Errorf("salt[0:2] = [%x, %x], want [31, 00]", salt[0], salt[1])
	}
	if salt[2] != 0x38 || salt[3] != 0x00 { // '8' in UTF-16LE
		t.Errorf("salt[2:4] = [%x, %x], want [38, 00]", salt[2], salt[3])
	}
}

func TestChecksumAndMagic(t *testing.T) {
	magic := Get24BitHash("TestSecurityKey!!")

	// Create a packet, stamp it, validate it
	p := &Packet{Type: Mouse, ID: 1, Src: 1, Des: 2}
	p.Mouse.X = 100
	p.Mouse.Y = 200
	buf := p.Marshal()

	StampPacket(buf, magic)

	// Verify magic bytes
	expectedHi := byte((magic >> 24) & 0xFF)
	expectedLo := byte((magic >> 16) & 0xFF)
	if buf[3] != expectedHi || buf[2] != expectedLo {
		t.Errorf("magic bytes = [%x, %x], want [%x, %x]", buf[2], buf[3], expectedLo, expectedHi)
	}

	// Validate should succeed
	if err := ValidatePacket(buf, magic); err != nil {
		t.Errorf("validation failed: %v", err)
	}

	// Corrupt a byte -> validation should fail
	buf[15] ^= 0xFF
	if err := ValidatePacket(buf, magic); err == nil {
		t.Error("validation should fail after corruption")
	}
}
```

**Step 3: Run test to verify it fails**

Run: `go test ./internal/protocol/ -v -run "TestDeriveKey|TestFixedIV|TestGet24BitHash|TestPBKDF2Salt|TestChecksumAndMagic"`
Expected: FAIL

**Step 4: Write implementation**

```go
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
	initialIVStr = "18446744073709551615" // ulong.MaxValue.ToString()
)

// pbkdf2Salt returns the UTF-16LE encoding of "18446744073709551615" (40 bytes).
// This matches C#: Common.GetBytesU(InitialIV) which uses Encoding.Unicode (UTF-16LE).
func pbkdf2Salt() []byte {
	s := initialIVStr
	salt := make([]byte, len(s)*2)
	for i, c := range s {
		binary.LittleEndian.PutUint16(salt[i*2:], uint16(c))
	}
	return salt
}

// DeriveKey derives a 32-byte AES-256 key from the user's security key
// using PBKDF2-SHA512 with 50,000 iterations.
func DeriveKey(securityKey string) []byte {
	return pbkdf2.Key([]byte(securityKey), pbkdf2Salt(), pbkdf2Iter, aesKeySize, sha512.New)
}

// FixedIV returns the 16-byte IV used for AES-CBC.
// This is the ASCII encoding of "1844674407370955" (first 16 chars of InitialIV).
func FixedIV() []byte {
	s := initialIVStr
	if len(s) > aesBlockSize {
		s = s[:aesBlockSize]
	}
	return []byte(s)
}

// Get24BitHash computes the MWB magic number from the security key.
// Algorithm: SHA-512 the key (zero-padded to 32 bytes), then iterate SHA-512
// 50,000 more times, then combine specific bytes.
func Get24BitHash(key string) uint32 {
	if key == "" {
		return 0
	}

	buf := make([]byte, PacketSize) // 32 bytes
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
// Must be called before encryption and sending.
// buf[0] = PackageType, buf[1] = checksum, buf[2:4] = magic number bytes.
func StampPacket(buf []byte, magic uint32) {
	buf[3] = byte((magic >> 24) & 0xFF)
	buf[2] = byte((magic >> 16) & 0xFF)
	buf[1] = 0
	for i := 2; i < PacketSize; i++ {
		buf[1] = buf[1] + buf[i]
	}
}

// ValidatePacket checks the magic number and checksum of a received packet.
// Returns nil if valid.
func ValidatePacket(buf []byte, magic uint32) error {
	// Check magic (top 16 bits)
	wireMagic := uint32(buf[3])<<24 + uint32(buf[2])<<16
	expectedMagic := magic & 0xFFFF0000
	if wireMagic != expectedMagic {
		return fmt.Errorf("magic mismatch: wire=0x%08X expected=0x%08X", wireMagic, expectedMagic)
	}

	// Check checksum
	var checksum byte
	for i := 2; i < PacketSize; i++ {
		checksum += buf[i]
	}
	if buf[1] != checksum {
		return fmt.Errorf("checksum mismatch: wire=0x%02X computed=0x%02X", buf[1], checksum)
	}

	return nil
}

// ClearStamp zeroes bytes 1-3 after validation, leaving only the PackageType in byte 0.
func ClearStamp(buf []byte) {
	buf[1] = 0
	buf[2] = 0
	buf[3] = 0
}
```

**Step 5: Run tests**

Run: `go test ./internal/protocol/ -v -run "TestDeriveKey|TestFixedIV|TestGet24BitHash|TestPBKDF2Salt|TestChecksumAndMagic"`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/protocol/crypto.go internal/protocol/crypto_test.go go.mod go.sum
git commit -m "feat: AES key derivation, magic number, checksum"
```

---

### Task 5: AES-256-CBC Encrypted Stream

**Files:**
- Create: `internal/protocol/stream.go`
- Create: `internal/protocol/stream_test.go`

**Step 1: Write the test**

```go
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

	plaintext := []byte("Hello, Mouse Without Borders!!!!")  // exactly 32 bytes
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
	if err := enc.Flush(); err != nil {
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
	enc.Write(pkt1)
	enc.Flush()
	enc.Write(pkt2)
	enc.Flush()

	dec, err := NewDecryptReader(bytes.NewReader(cipherBuf.Bytes()), key, iv)
	if err != nil {
		t.Fatal(err)
	}

	got1 := make([]byte, 32)
	io.ReadFull(dec, got1)
	got2 := make([]byte, 32)
	io.ReadFull(dec, got2)

	if !bytes.Equal(got1, pkt1) {
		t.Error("packet 1 mismatch")
	}
	if !bytes.Equal(got2, pkt2) {
		t.Error("packet 2 mismatch")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/protocol/ -v -run TestEncrypt`
Expected: FAIL

**Step 3: Write implementation**

```go
// internal/protocol/stream.go
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
)

// EncryptWriter wraps an io.Writer with AES-256-CBC encryption.
// MWB uses zero-padding. Since all packets are multiples of 16 bytes,
// no padding is actually needed; we encrypt block-by-block.
type EncryptWriter struct {
	w      io.Writer
	mode   cipher.BlockMode
	block  cipher.Block
}

// NewEncryptWriter creates an AES-256-CBC encrypting writer.
func NewEncryptWriter(w io.Writer, key, iv []byte) (*EncryptWriter, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	return &EncryptWriter{w: w, mode: mode, block: block}, nil
}

// Write encrypts data and writes to the underlying writer.
// Data length must be a multiple of 16 (AES block size).
func (e *EncryptWriter) Write(p []byte) (int, error) {
	if len(p)%aesBlockSize != 0 {
		return 0, fmt.Errorf("data length %d not multiple of %d", len(p), aesBlockSize)
	}
	ct := make([]byte, len(p))
	e.mode.CryptBlocks(ct, p)
	return e.w.Write(ct)
}

// Flush is a no-op for compatibility; CBC writes are immediate.
func (e *EncryptWriter) Flush() error {
	return nil
}

// DecryptReader wraps an io.Reader with AES-256-CBC decryption.
type DecryptReader struct {
	r    io.Reader
	mode cipher.BlockMode
}

// NewDecryptReader creates an AES-256-CBC decrypting reader.
func NewDecryptReader(r io.Reader, key, iv []byte) (*DecryptReader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	return &DecryptReader{r: r, mode: mode}, nil
}

// Read reads encrypted data, decrypts it, and returns plaintext.
// Reads in multiples of 16 bytes.
func (d *DecryptReader) Read(p []byte) (int, error) {
	// Round down to block size
	n := len(p)
	if n < aesBlockSize {
		n = aesBlockSize
		buf := make([]byte, n)
		nn, err := io.ReadFull(d.r, buf)
		if err != nil {
			return 0, err
		}
		d.mode.CryptBlocks(buf[:nn], buf[:nn])
		copy(p, buf[:nn])
		return min(len(p), nn), nil
	}

	n = (n / aesBlockSize) * aesBlockSize
	ct := make([]byte, n)
	nn, err := io.ReadFull(d.r, ct)
	if err != nil {
		return 0, err
	}
	d.mode.CryptBlocks(ct[:nn], ct[:nn])
	copy(p, ct[:nn])
	return nn, nil
}
```

**Step 4: Run tests**

Run: `go test ./internal/protocol/ -v -run TestEncrypt`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/protocol/stream.go internal/protocol/stream_test.go
git commit -m "feat: AES-256-CBC encrypted stream reader/writer"
```

---

### Task 6: Configuration

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Step 1: Add TOML dependency**

Run: `cd /home/bjk/projects/scratch/mwb && go get github.com/BurntSushi/toml`

**Step 2: Write the test**

```go
// internal/config/config_test.go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
host = "192.168.1.50"
key = "MySecurityKey1234"
name = "testbox"
port = 15100
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "192.168.1.50" {
		t.Errorf("host = %q, want %q", cfg.Host, "192.168.1.50")
	}
	if cfg.Key != "MySecurityKey1234" {
		t.Errorf("key = %q", cfg.Key)
	}
	if cfg.Name != "testbox" {
		t.Errorf("name = %q", cfg.Name)
	}
	if cfg.Port != 15100 {
		t.Errorf("port = %d, want 15100", cfg.Port)
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	os.WriteFile(path, []byte(`
host = "10.0.0.1"
key = "SomeKeyHere!1234"
`), 0644)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 15100 {
		t.Errorf("default port = %d, want 15100", cfg.Port)
	}
	if cfg.Name == "" {
		t.Error("name should default to hostname")
	}
}

func TestLoadConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	// Missing host
	os.WriteFile(path, []byte(`key = "SomeKeyHere!1234"`), 0644)
	if _, err := Load(path); err == nil {
		t.Error("should fail without host")
	}

	// Missing key
	os.WriteFile(path, []byte(`host = "10.0.0.1"`), 0644)
	if _, err := Load(path); err == nil {
		t.Error("should fail without key")
	}
}
```

**Step 3: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL

**Step 4: Write implementation**

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Config holds the mwb client configuration.
type Config struct {
	Host string `toml:"host"`
	Key  string `toml:"key"`
	Name string `toml:"name"`
	Port int    `toml:"port"`
}

// Load reads and validates a TOML config file.
func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if cfg.Host == "" {
		return nil, fmt.Errorf("config: host is required")
	}
	if cfg.Key == "" {
		return nil, fmt.Errorf("config: key is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 15100
	}
	if cfg.Name == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "linux"
		}
		cfg.Name = hostname
	}
	// Truncate name to 15 chars (NetBIOS limit)
	if len(cfg.Name) > 15 {
		cfg.Name = cfg.Name[:15]
	}

	return &cfg, nil
}

// MessagePort returns the TCP port for the message channel (base port + 1).
func (c *Config) MessagePort() int {
	return c.Port + 1
}
```

**Step 5: Run tests**

Run: `go test ./internal/config/ -v`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/config/ go.mod go.sum
git commit -m "feat: TOML configuration loading"
```

---

### Task 7: VK-to-Evdev Keycode Mapping

**Files:**
- Create: `internal/input/keymap.go`
- Create: `internal/input/keymap_test.go`

**Step 1: Write the test**

```go
// internal/input/keymap_test.go
package input

import "testing"

func TestVKToEvdev(t *testing.T) {
	tests := []struct {
		name string
		vk   int32
		want uint16
	}{
		{"VK_A", 0x41, KEY_A},
		{"VK_Z", 0x5A, KEY_Z},
		{"VK_0", 0x30, KEY_0},
		{"VK_9", 0x39, KEY_9},
		{"VK_RETURN", 0x0D, KEY_ENTER},
		{"VK_ESCAPE", 0x1B, KEY_ESC},
		{"VK_SPACE", 0x20, KEY_SPACE},
		{"VK_TAB", 0x09, KEY_TAB},
		{"VK_BACK", 0x08, KEY_BACKSPACE},
		{"VK_LSHIFT", 0xA0, KEY_LEFTSHIFT},
		{"VK_RSHIFT", 0xA1, KEY_RIGHTSHIFT},
		{"VK_LCONTROL", 0xA2, KEY_LEFTCTRL},
		{"VK_RCONTROL", 0xA3, KEY_RIGHTCTRL},
		{"VK_LMENU", 0xA4, KEY_LEFTALT},
		{"VK_RMENU", 0xA5, KEY_RIGHTALT},
		{"VK_LWIN", 0x5B, KEY_LEFTMETA},
		{"VK_F1", 0x70, KEY_F1},
		{"VK_F12", 0x7B, KEY_F12},
		{"VK_LEFT", 0x25, KEY_LEFT},
		{"VK_UP", 0x26, KEY_UP},
		{"VK_RIGHT", 0x27, KEY_RIGHT},
		{"VK_DOWN", 0x28, KEY_DOWN},
		{"VK_DELETE", 0x2E, KEY_DELETE},
		{"VK_INSERT", 0x2D, KEY_INSERT},
		{"VK_HOME", 0x24, KEY_HOME},
		{"VK_END", 0x23, KEY_END},
		{"VK_PRIOR", 0x21, KEY_PAGEUP},
		{"VK_NEXT", 0x22, KEY_PAGEDOWN},
		{"unknown", 0xFFF, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := VKToEvdev(tt.vk)
			if tt.want == 0 {
				if ok {
					t.Errorf("expected unknown for VK 0x%X", tt.vk)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("VKToEvdev(0x%X) = %d, %v; want %d, true", tt.vk, got, ok, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/input/ -v -run TestVKToEvdev`
Expected: FAIL

**Step 3: Write implementation**

```go
// internal/input/keymap.go
package input

// Linux evdev key codes (from /usr/include/linux/input-event-codes.h)
const (
	KEY_ESC            uint16 = 1
	KEY_1              uint16 = 2
	KEY_2              uint16 = 3
	KEY_3              uint16 = 4
	KEY_4              uint16 = 5
	KEY_5              uint16 = 6
	KEY_6              uint16 = 7
	KEY_7              uint16 = 8
	KEY_8              uint16 = 9
	KEY_9              uint16 = 10
	KEY_0              uint16 = 11
	KEY_MINUS          uint16 = 12
	KEY_EQUAL          uint16 = 13
	KEY_BACKSPACE      uint16 = 14
	KEY_TAB            uint16 = 15
	KEY_Q              uint16 = 16
	KEY_W              uint16 = 17
	KEY_E              uint16 = 18
	KEY_R              uint16 = 19
	KEY_T              uint16 = 20
	KEY_Y              uint16 = 21
	KEY_U              uint16 = 22
	KEY_I              uint16 = 23
	KEY_O              uint16 = 24
	KEY_P              uint16 = 25
	KEY_LEFTBRACE      uint16 = 26
	KEY_RIGHTBRACE     uint16 = 27
	KEY_ENTER          uint16 = 28
	KEY_LEFTCTRL       uint16 = 29
	KEY_A              uint16 = 30
	KEY_S              uint16 = 31
	KEY_D              uint16 = 32
	KEY_F              uint16 = 33
	KEY_G              uint16 = 34
	KEY_H              uint16 = 35
	KEY_J              uint16 = 36
	KEY_K              uint16 = 37
	KEY_L              uint16 = 38
	KEY_SEMICOLON      uint16 = 39
	KEY_APOSTROPHE     uint16 = 40
	KEY_GRAVE          uint16 = 41
	KEY_LEFTSHIFT      uint16 = 42
	KEY_BACKSLASH      uint16 = 43
	KEY_Z              uint16 = 44
	KEY_X              uint16 = 45
	KEY_C              uint16 = 46
	KEY_V              uint16 = 47
	KEY_B              uint16 = 48
	KEY_N              uint16 = 49
	KEY_M              uint16 = 50
	KEY_COMMA          uint16 = 51
	KEY_DOT            uint16 = 52
	KEY_SLASH          uint16 = 53
	KEY_RIGHTSHIFT     uint16 = 54
	KEY_KPASTERISK     uint16 = 55
	KEY_LEFTALT        uint16 = 56
	KEY_SPACE          uint16 = 57
	KEY_CAPSLOCK       uint16 = 58
	KEY_F1             uint16 = 59
	KEY_F2             uint16 = 60
	KEY_F3             uint16 = 61
	KEY_F4             uint16 = 62
	KEY_F5             uint16 = 63
	KEY_F6             uint16 = 64
	KEY_F7             uint16 = 65
	KEY_F8             uint16 = 66
	KEY_F9             uint16 = 67
	KEY_F10            uint16 = 68
	KEY_NUMLOCK        uint16 = 69
	KEY_SCROLLLOCK     uint16 = 70
	KEY_KP7            uint16 = 71
	KEY_KP8            uint16 = 72
	KEY_KP9            uint16 = 73
	KEY_KPMINUS        uint16 = 74
	KEY_KP4            uint16 = 75
	KEY_KP5            uint16 = 76
	KEY_KP6            uint16 = 77
	KEY_KPPLUS         uint16 = 78
	KEY_KP1            uint16 = 79
	KEY_KP2            uint16 = 80
	KEY_KP3            uint16 = 81
	KEY_KP0            uint16 = 82
	KEY_KPDOT          uint16 = 83
	KEY_F11            uint16 = 87
	KEY_F12            uint16 = 88
	KEY_KPENTER        uint16 = 96
	KEY_RIGHTCTRL      uint16 = 97
	KEY_KPSLASH        uint16 = 98
	KEY_SYSRQ          uint16 = 99
	KEY_RIGHTALT       uint16 = 100
	KEY_HOME           uint16 = 102
	KEY_UP             uint16 = 103
	KEY_PAGEUP         uint16 = 104
	KEY_LEFT           uint16 = 105
	KEY_RIGHT          uint16 = 106
	KEY_END            uint16 = 107
	KEY_DOWN           uint16 = 108
	KEY_PAGEDOWN       uint16 = 109
	KEY_INSERT         uint16 = 110
	KEY_DELETE         uint16 = 111
	KEY_MUTE           uint16 = 113
	KEY_VOLUMEDOWN     uint16 = 114
	KEY_VOLUMEUP       uint16 = 115
	KEY_PAUSE          uint16 = 119
	KEY_LEFTMETA       uint16 = 125
	KEY_RIGHTMETA      uint16 = 126
	KEY_COMPOSE        uint16 = 127
	KEY_MAX            uint16 = 0x2ff
)

// Windows Virtual Key codes
const (
	VK_BACK     = 0x08
	VK_TAB      = 0x09
	VK_RETURN   = 0x0D
	VK_PAUSE    = 0x13
	VK_CAPITAL  = 0x14
	VK_ESCAPE   = 0x1B
	VK_SPACE    = 0x20
	VK_PRIOR    = 0x21
	VK_NEXT     = 0x22
	VK_END      = 0x23
	VK_HOME     = 0x24
	VK_LEFT     = 0x25
	VK_UP       = 0x26
	VK_RIGHT    = 0x27
	VK_DOWN     = 0x28
	VK_SNAPSHOT = 0x2C
	VK_INSERT   = 0x2D
	VK_DELETE   = 0x2E
	VK_LWIN     = 0x5B
	VK_RWIN     = 0x5C
	VK_APPS     = 0x5D
	VK_NUMPAD0  = 0x60
	VK_NUMPAD9  = 0x69
	VK_MULTIPLY = 0x6A
	VK_ADD      = 0x6B
	VK_SUBTRACT = 0x6D
	VK_DECIMAL  = 0x6E
	VK_DIVIDE   = 0x6F
	VK_F1       = 0x70
	VK_F12      = 0x7B
	VK_NUMLOCK  = 0x90
	VK_SCROLL   = 0x91
	VK_LSHIFT   = 0xA0
	VK_RSHIFT   = 0xA1
	VK_LCONTROL = 0xA2
	VK_RCONTROL = 0xA3
	VK_LMENU    = 0xA4
	VK_RMENU    = 0xA5
	VK_VOLUME_MUTE = 0xAD
	VK_VOLUME_DOWN = 0xAE
	VK_VOLUME_UP   = 0xAF
	VK_OEM_1    = 0xBA // ;:
	VK_OEM_PLUS = 0xBB // =+
	VK_OEM_COMMA = 0xBC // ,<
	VK_OEM_MINUS = 0xBD // -_
	VK_OEM_PERIOD = 0xBE // .>
	VK_OEM_2    = 0xBF // /?
	VK_OEM_3    = 0xC0 // `~
	VK_OEM_4    = 0xDB // [{
	VK_OEM_5    = 0xDC // \|
	VK_OEM_6    = 0xDD // ]}
	VK_OEM_7    = 0xDE // '"
)

// vkMap maps Windows VK codes to Linux evdev KEY_ codes.
var vkMap = map[int32]uint16{
	VK_BACK:       KEY_BACKSPACE,
	VK_TAB:        KEY_TAB,
	VK_RETURN:     KEY_ENTER,
	VK_PAUSE:      KEY_PAUSE,
	VK_CAPITAL:    KEY_CAPSLOCK,
	VK_ESCAPE:     KEY_ESC,
	VK_SPACE:      KEY_SPACE,
	VK_PRIOR:      KEY_PAGEUP,
	VK_NEXT:       KEY_PAGEDOWN,
	VK_END:        KEY_END,
	VK_HOME:       KEY_HOME,
	VK_LEFT:       KEY_LEFT,
	VK_UP:         KEY_UP,
	VK_RIGHT:      KEY_RIGHT,
	VK_DOWN:       KEY_DOWN,
	VK_SNAPSHOT:   KEY_SYSRQ,
	VK_INSERT:     KEY_INSERT,
	VK_DELETE:     KEY_DELETE,
	VK_LWIN:       KEY_LEFTMETA,
	VK_RWIN:       KEY_RIGHTMETA,
	VK_APPS:       KEY_COMPOSE,
	VK_NUMPAD0:    KEY_KP0,
	0x61:          KEY_KP1,
	0x62:          KEY_KP2,
	0x63:          KEY_KP3,
	0x64:          KEY_KP4,
	0x65:          KEY_KP5,
	0x66:          KEY_KP6,
	0x67:          KEY_KP7,
	0x68:          KEY_KP8,
	VK_NUMPAD9:    KEY_KP9,
	VK_MULTIPLY:   KEY_KPASTERISK,
	VK_ADD:        KEY_KPPLUS,
	VK_SUBTRACT:   KEY_KPMINUS,
	VK_DECIMAL:    KEY_KPDOT,
	VK_DIVIDE:     KEY_KPSLASH,
	VK_F1:         KEY_F1,
	0x71:          KEY_F2,
	0x72:          KEY_F3,
	0x73:          KEY_F4,
	0x74:          KEY_F5,
	0x75:          KEY_F6,
	0x76:          KEY_F7,
	0x77:          KEY_F8,
	0x78:          KEY_F9,
	0x79:          KEY_F10,
	0x7A:          KEY_F11,
	VK_F12:        KEY_F12,
	VK_NUMLOCK:    KEY_NUMLOCK,
	VK_SCROLL:     KEY_SCROLLLOCK,
	VK_LSHIFT:     KEY_LEFTSHIFT,
	VK_RSHIFT:     KEY_RIGHTSHIFT,
	VK_LCONTROL:   KEY_LEFTCTRL,
	VK_RCONTROL:   KEY_RIGHTCTRL,
	VK_LMENU:      KEY_LEFTALT,
	VK_RMENU:      KEY_RIGHTALT,
	VK_VOLUME_MUTE: KEY_MUTE,
	VK_VOLUME_DOWN: KEY_VOLUMEDOWN,
	VK_VOLUME_UP:   KEY_VOLUMEUP,
	VK_OEM_1:      KEY_SEMICOLON,
	VK_OEM_PLUS:   KEY_EQUAL,
	VK_OEM_COMMA:  KEY_COMMA,
	VK_OEM_MINUS:  KEY_MINUS,
	VK_OEM_PERIOD: KEY_DOT,
	VK_OEM_2:      KEY_SLASH,
	VK_OEM_3:      KEY_GRAVE,
	VK_OEM_4:      KEY_LEFTBRACE,
	VK_OEM_5:      KEY_BACKSLASH,
	VK_OEM_6:      KEY_RIGHTBRACE,
	VK_OEM_7:      KEY_APOSTROPHE,
}

func init() {
	// A-Z: VK 0x41-0x5A -> KEY_A(30) - KEY_Z(44)
	for vk := int32(0x41); vk <= 0x5A; vk++ {
		// VK_A=0x41 -> KEY_A=30, etc.
		// The mapping isn't contiguous in evdev, so use a lookup
		letters := []uint16{
			KEY_A, KEY_B, KEY_C, KEY_D, KEY_E, KEY_F, KEY_G, KEY_H, KEY_I,
			KEY_J, KEY_K, KEY_L, KEY_M, KEY_N, KEY_O, KEY_P, KEY_Q, KEY_R,
			KEY_S, KEY_T, KEY_U, KEY_V, KEY_W, KEY_X, KEY_Y, KEY_Z,
		}
		vkMap[vk] = letters[vk-0x41]
	}
	// 0-9: VK 0x30-0x39 -> KEY_1(2)-KEY_0(11)
	// Note: Windows VK_0=0x30 maps to KEY_0=11 (evdev puts 0 after 9)
	digits := []uint16{KEY_0, KEY_1, KEY_2, KEY_3, KEY_4, KEY_5, KEY_6, KEY_7, KEY_8, KEY_9}
	for vk := int32(0x30); vk <= 0x39; vk++ {
		vkMap[vk] = digits[vk-0x30]
	}
}

// VKToEvdev maps a Windows Virtual Key code to a Linux evdev key code.
// Returns the evdev code and true if found, or 0 and false if unknown.
func VKToEvdev(vk int32) (uint16, bool) {
	code, ok := vkMap[vk]
	return code, ok
}
```

**Step 4: Run tests**

Run: `go test ./internal/input/ -v -run TestVKToEvdev`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/input/keymap.go internal/input/keymap_test.go
git commit -m "feat: VK-to-evdev keycode mapping table"
```

---

### Task 8: uinput Virtual Devices

**Files:**
- Create: `internal/input/uinput.go`
- Create: `internal/input/uinput_test.go`

Note: Tests for uinput require `/dev/uinput` access (input group or root). Tests that can't access it should be skipped.

**Step 1: Write the test**

```go
// internal/input/uinput_test.go
//go:build linux

package input

import (
	"os"
	"testing"
)

func canAccessUinput(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("skipping: cannot access /dev/uinput: %v", err)
	}
	f.Close()
}

func TestCreateVirtualMouse(t *testing.T) {
	canAccessUinput(t)

	m, err := CreateVirtualMouse("mwb-test-mouse")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// Move to center
	if err := m.MoveTo(32768, 32768); err != nil {
		t.Errorf("MoveTo: %v", err)
	}

	// Scroll
	if err := m.Wheel(1); err != nil {
		t.Errorf("Wheel: %v", err)
	}
}

func TestCreateVirtualKeyboard(t *testing.T) {
	canAccessUinput(t)

	k, err := CreateVirtualKeyboard("mwb-test-kbd")
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	// Press and release a key
	if err := k.KeyDown(KEY_A); err != nil {
		t.Errorf("KeyDown: %v", err)
	}
	if err := k.KeyUp(KEY_A); err != nil {
		t.Errorf("KeyUp: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/input/ -v -run "TestCreateVirtual"`
Expected: FAIL (functions not defined)

**Step 3: Write implementation**

```go
// internal/input/uinput.go
//go:build linux

package input

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// ioctl constants (from /usr/include/linux/uinput.h)
const (
	uiDevCreate  uintptr = 0x5501
	uiDevDestroy uintptr = 0x5502
	uiDevSetup   uintptr = 0x405c5503
	uiAbsSetup   uintptr = 0x401c5504
	uiSetEvBit   uintptr = 0x40045564
	uiSetKeyBit  uintptr = 0x40045565
	uiSetRelBit  uintptr = 0x40045566
	uiSetAbsBit  uintptr = 0x40045567
)

// Event types
const (
	evSyn uint16 = 0x00
	evKey uint16 = 0x01
	evRel uint16 = 0x02
	evAbs uint16 = 0x03
)

// Buttons
const (
	BTN_LEFT   uint16 = 0x110
	BTN_RIGHT  uint16 = 0x111
	BTN_MIDDLE uint16 = 0x112
)

// Relative axes
const (
	relWheel  uint16 = 0x08
	relHWheel uint16 = 0x06
)

// Absolute axes
const (
	absX    uint16 = 0x00
	absY    uint16 = 0x01
	absCnt         = 0x40
)

const busVirtual uint16 = 0x06

// inputEvent matches struct input_event (24 bytes on 64-bit).
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

type inputID struct {
	Bustype uint16
	Vendor  uint16
	Product uint16
	Version uint16
}

type uinputSetup struct {
	ID           inputID
	Name         [80]byte
	FFEffectsMax uint32
}

type inputAbsinfo struct {
	Value      int32
	Minimum    int32
	Maximum    int32
	Fuzz       int32
	Flat       int32
	Resolution int32
}

type uinputAbsSetup struct {
	Code uint16
	_    uint16
	inputAbsinfo
}

type uinputUserDev struct {
	Name         [80]byte
	ID           inputID
	FFEffectsMax uint32
	Absmax       [absCnt]int32
	Absmin       [absCnt]int32
	Absfuzz      [absCnt]int32
	Absflat      [absCnt]int32
}

func ioctl(fd *os.File, request, value uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd.Fd(), request, value)
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlPtr(fd *os.File, request uintptr, ptr unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd.Fd(), request, uintptr(ptr))
	if errno != 0 {
		return errno
	}
	return nil
}

func writeEvent(fd *os.File, typ, code uint16, value int32) error {
	ev := inputEvent{Type: typ, Code: code, Value: value}
	return binary.Write(fd, binary.LittleEndian, &ev)
}

func syncEvents(fd *os.File) error {
	return writeEvent(fd, evSyn, 0, 0)
}

// VirtualMouse is a uinput device with absolute positioning, buttons, and wheel.
type VirtualMouse struct {
	fd *os.File
}

// CreateVirtualMouse creates a virtual mouse with ABS_X/Y (0-65535), buttons, and wheel.
func CreateVirtualMouse(name string) (*VirtualMouse, error) {
	fd, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/uinput: %w (ensure user is in 'input' group)", err)
	}

	ok := false
	defer func() {
		if !ok {
			fd.Close()
		}
	}()

	// Register event types
	for _, ev := range []uintptr{uintptr(evKey), uintptr(evAbs), uintptr(evRel)} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
		}
	}

	// Buttons
	for _, btn := range []uintptr{uintptr(BTN_LEFT), uintptr(BTN_RIGHT), uintptr(BTN_MIDDLE)} {
		if err := ioctl(fd, uiSetKeyBit, btn); err != nil {
			return nil, fmt.Errorf("UI_SET_KEYBIT: %w", err)
		}
	}

	// Absolute axes
	for _, axis := range []uintptr{uintptr(absX), uintptr(absY)} {
		if err := ioctl(fd, uiSetAbsBit, axis); err != nil {
			return nil, fmt.Errorf("UI_SET_ABSBIT: %w", err)
		}
	}

	// Relative axis (wheel)
	if err := ioctl(fd, uiSetRelBit, uintptr(relWheel)); err != nil {
		return nil, fmt.Errorf("UI_SET_RELBIT: %w", err)
	}

	// Device setup - try new API, fall back to legacy
	if err := setupMouseNew(fd, name); err != nil {
		if err := setupMouseLegacy(fd, name); err != nil {
			return nil, fmt.Errorf("device setup: %w", err)
		}
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return nil, fmt.Errorf("UI_DEV_CREATE: %w", err)
	}

	ok = true
	return &VirtualMouse{fd: fd}, nil
}

func setupMouseNew(fd *os.File, name string) error {
	setup := uinputSetup{ID: inputID{Bustype: busVirtual, Vendor: 0x4D57, Product: 0x4231, Version: 1}}
	copy(setup.Name[:], name)
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		return err
	}
	for _, axis := range []uint16{absX, absY} {
		as := uinputAbsSetup{Code: axis, inputAbsinfo: inputAbsinfo{Maximum: 65535}}
		if err := ioctlPtr(fd, uiAbsSetup, unsafe.Pointer(&as)); err != nil {
			return err
		}
	}
	return nil
}

func setupMouseLegacy(fd *os.File, name string) error {
	dev := uinputUserDev{ID: inputID{Bustype: busVirtual, Vendor: 0x4D57, Product: 0x4231, Version: 1}}
	copy(dev.Name[:], name)
	dev.Absmax[absX] = 65535
	dev.Absmax[absY] = 65535
	return binary.Write(fd, binary.LittleEndian, &dev)
}

func (m *VirtualMouse) MoveTo(x, y int32) error {
	if err := writeEvent(m.fd, evAbs, absX, x); err != nil {
		return err
	}
	if err := writeEvent(m.fd, evAbs, absY, y); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

func (m *VirtualMouse) ButtonDown(button uint16) error {
	if err := writeEvent(m.fd, evKey, button, 1); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

func (m *VirtualMouse) ButtonUp(button uint16) error {
	if err := writeEvent(m.fd, evKey, button, 0); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

func (m *VirtualMouse) Wheel(delta int32) error {
	if err := writeEvent(m.fd, evRel, relWheel, delta); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

func (m *VirtualMouse) Close() error {
	ioctl(m.fd, uiDevDestroy, 0)
	return m.fd.Close()
}

// VirtualKeyboard is a uinput keyboard device.
type VirtualKeyboard struct {
	fd *os.File
}

// CreateVirtualKeyboard creates a virtual keyboard supporting all standard keys.
func CreateVirtualKeyboard(name string) (*VirtualKeyboard, error) {
	fd, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/uinput: %w (ensure user is in 'input' group)", err)
	}

	ok := false
	defer func() {
		if !ok {
			fd.Close()
		}
	}()

	if err := ioctl(fd, uiSetEvBit, uintptr(evKey)); err != nil {
		return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
	}

	for i := uintptr(1); i <= uintptr(KEY_MAX); i++ {
		if err := ioctl(fd, uiSetKeyBit, i); err != nil {
			return nil, fmt.Errorf("UI_SET_KEYBIT %d: %w", i, err)
		}
	}

	setup := uinputSetup{ID: inputID{Bustype: busVirtual, Vendor: 0x4D57, Product: 0x4B31, Version: 1}}
	copy(setup.Name[:], name)
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
		// Legacy fallback
		dev := uinputUserDev{ID: inputID{Bustype: busVirtual, Vendor: 0x4D57, Product: 0x4B31, Version: 1}}
		copy(dev.Name[:], name)
		if err := binary.Write(fd, binary.LittleEndian, &dev); err != nil {
			return nil, fmt.Errorf("device setup: %w", err)
		}
	}

	if err := ioctl(fd, uiDevCreate, 0); err != nil {
		return nil, fmt.Errorf("UI_DEV_CREATE: %w", err)
	}

	ok = true
	return &VirtualKeyboard{fd: fd}, nil
}

func (k *VirtualKeyboard) KeyDown(code uint16) error {
	if err := writeEvent(k.fd, evKey, code, 1); err != nil {
		return err
	}
	return syncEvents(k.fd)
}

func (k *VirtualKeyboard) KeyUp(code uint16) error {
	if err := writeEvent(k.fd, evKey, code, 0); err != nil {
		return err
	}
	return syncEvents(k.fd)
}

func (k *VirtualKeyboard) Close() error {
	ioctl(k.fd, uiDevDestroy, 0)
	return k.fd.Close()
}
```

**Step 4: Run tests**

Run: `go test ./internal/input/ -v -run "TestCreateVirtual"`
Expected: PASS (or SKIP if no /dev/uinput access)

**Step 5: Commit**

```bash
git add internal/input/uinput.go internal/input/uinput_test.go
git commit -m "feat: uinput virtual mouse and keyboard devices"
```

---

### Task 9: Network Client - TCP Connection and Encrypted Handshake

**Files:**
- Create: `internal/network/client.go`
- Create: `internal/network/client_test.go`

**Step 1: Write the test**

```go
// internal/network/client_test.go
package network

import (
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bjk/mwb/internal/protocol"
)

func TestConnectionHandshake(t *testing.T) {
	securityKey := "TestSecurityKey!!"
	aesKey := protocol.DeriveKey(securityKey)
	iv := protocol.FixedIV()
	magic := protocol.Get24BitHash(securityKey)

	// Start a fake MWB server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()

		// Server: create encrypted streams
		enc, err := protocol.NewEncryptWriter(conn, aesKey, iv)
		if err != nil {
			serverDone <- err
			return
		}
		dec, err := protocol.NewDecryptReader(conn, aesKey, iv)
		if err != nil {
			serverDone <- err
			return
		}

		// Server: send random IV block
		ranData := make([]byte, 16)
		rand.Read(ranData)
		enc.Write(ranData)

		// Server: read random IV block from client
		clientRan := make([]byte, 16)
		io.ReadFull(dec, clientRan)

		// Server: read handshake from client (64 bytes for big packet)
		pktBuf := make([]byte, protocol.PacketSizeEx)
		if _, err := io.ReadFull(dec, pktBuf); err != nil {
			serverDone <- err
			return
		}

		if err := protocol.ValidatePacket(pktBuf, magic); err != nil {
			serverDone <- err
			return
		}
		protocol.ClearStamp(pktBuf)

		pkt, err := protocol.UnmarshalPacket(pktBuf)
		if err != nil {
			serverDone <- err
			return
		}

		if pkt.Type != protocol.Handshake {
			serverDone <- err
			return
		}

		// Server: send HandshakeAck with inverted machine fields
		ack := &protocol.Packet{
			Type: protocol.HandshakeAck,
			Src:  0,
			Des:  pkt.Src,
		}
		ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
		ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
		ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
		ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
		ack.SetMachineName("WINHOST")

		ackBuf := ack.Marshal()
		protocol.StampPacket(ackBuf, magic)
		enc.Write(ackBuf)

		serverDone <- nil
	}()

	// Client: connect and handshake
	addr := ln.Addr().String()
	conn, err := Connect(addr, securityKey, "linux-test", 1*time.Second)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer conn.Close()

	if conn.RemoteName == "" {
		t.Log("warning: remote name not set (may depend on impl)")
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for server")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/network/ -v -run TestConnectionHandshake -timeout 10s`
Expected: FAIL

**Step 3: Write implementation**

```go
// internal/network/client.go
package network

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/bjk/mwb/internal/protocol"
)

// Conn represents an established, encrypted MWB connection.
type Conn struct {
	raw        net.Conn
	enc        *protocol.EncryptWriter
	dec        *protocol.DecryptReader
	magic      uint32
	machineID  uint32
	RemoteName string
}

// Connect establishes a TCP connection, performs IV exchange and handshake.
func Connect(addr, securityKey, machineName string, timeout time.Duration) (*Conn, error) {
	aesKey := protocol.DeriveKey(securityKey)
	iv := protocol.FixedIV()
	magic := protocol.Get24BitHash(securityKey)

	// TCP connect
	raw, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	ok := false
	defer func() {
		if !ok {
			raw.Close()
		}
	}()

	raw.(*net.TCPConn).SetNoDelay(true)

	// Create encrypted streams
	enc, err := protocol.NewEncryptWriter(raw, aesKey, iv)
	if err != nil {
		return nil, fmt.Errorf("encrypt stream: %w", err)
	}
	dec, err := protocol.NewDecryptReader(raw, aesKey, iv)
	if err != nil {
		return nil, fmt.Errorf("decrypt stream: %w", err)
	}

	// IV exchange: send random 16-byte block, read peer's random block
	ranData := make([]byte, 16)
	if _, err := rand.Read(ranData); err != nil {
		return nil, fmt.Errorf("rand: %w", err)
	}
	if _, err := enc.Write(ranData); err != nil {
		return nil, fmt.Errorf("send IV block: %w", err)
	}

	peerRan := make([]byte, 16)
	if _, err := io.ReadFull(dec, peerRan); err != nil {
		return nil, fmt.Errorf("read IV block: %w", err)
	}

	// Generate a machine ID from random data
	var machineID uint32
	machineID = uint32(ranData[0])<<24 | uint32(ranData[1])<<16 | uint32(ranData[2])<<8 | uint32(ranData[3])
	if machineID == 0 || machineID == 255 {
		machineID = 1
	}

	c := &Conn{
		raw:       raw,
		enc:       enc,
		dec:       dec,
		magic:     magic,
		machineID: machineID,
	}

	// Send handshake
	if err := c.doHandshake(machineName); err != nil {
		return nil, fmt.Errorf("handshake: %w", err)
	}

	ok = true
	return c, nil
}

func (c *Conn) doHandshake(machineName string) error {
	// Build handshake with random challenge data
	hs := &protocol.Packet{
		Type: protocol.Handshake,
		ID:   1,
		Src:  c.machineID,
		Des:  0,
	}

	// Random machine fields for challenge
	challenge := make([]byte, 16)
	rand.Read(challenge)
	hs.Handshake.Machine1 = uint32(challenge[0])<<24 | uint32(challenge[1])<<16 | uint32(challenge[2])<<8 | uint32(challenge[3])
	hs.Handshake.Machine2 = uint32(challenge[4])<<24 | uint32(challenge[5])<<16 | uint32(challenge[6])<<8 | uint32(challenge[7])
	hs.Handshake.Machine3 = uint32(challenge[8])<<24 | uint32(challenge[9])<<16 | uint32(challenge[10])<<8 | uint32(challenge[11])
	hs.Handshake.Machine4 = uint32(challenge[12])<<24 | uint32(challenge[13])<<16 | uint32(challenge[14])<<8 | uint32(challenge[15])
	hs.SetMachineName(machineName)

	// Expected response: bitwise inverted fields
	expect1 := ^hs.Handshake.Machine1
	expect2 := ^hs.Handshake.Machine2
	expect3 := ^hs.Handshake.Machine3
	expect4 := ^hs.Handshake.Machine4

	// Send 10 handshake packets (per MWB protocol)
	for i := 0; i < 10; i++ {
		if err := c.SendPacket(hs); err != nil {
			return fmt.Errorf("send handshake %d: %w", i, err)
		}
	}

	// Read packets until we get a HandshakeAck
	c.raw.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer c.raw.SetReadDeadline(time.Time{})

	for i := 0; i < 20; i++ {
		pkt, err := c.RecvPacket()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		if pkt.Type == protocol.Handshake {
			// Peer's handshake; respond with ACK
			ack := &protocol.Packet{
				Type: protocol.HandshakeAck,
				Src:  0,
				Des:  pkt.Src,
			}
			ack.Handshake.Machine1 = ^pkt.Handshake.Machine1
			ack.Handshake.Machine2 = ^pkt.Handshake.Machine2
			ack.Handshake.Machine3 = ^pkt.Handshake.Machine3
			ack.Handshake.Machine4 = ^pkt.Handshake.Machine4
			ack.SetMachineName(machineName)
			c.SendPacket(ack)
			continue
		}

		if pkt.Type == protocol.HandshakeAck {
			if pkt.Handshake.Machine1 == expect1 &&
				pkt.Handshake.Machine2 == expect2 &&
				pkt.Handshake.Machine3 == expect3 &&
				pkt.Handshake.Machine4 == expect4 {
				c.RemoteName = pkt.MachineName()
				slog.Info("handshake complete", "remote", c.RemoteName)
				return nil
			}
			return fmt.Errorf("handshake verification failed")
		}
	}

	return fmt.Errorf("no HandshakeAck received")
}

// SendPacket marshals, stamps, and sends a packet.
func (c *Conn) SendPacket(p *protocol.Packet) error {
	buf := p.Marshal()
	protocol.StampPacket(buf, c.magic)
	_, err := c.enc.Write(buf)
	return err
}

// RecvPacket reads, validates, and unmarshals a packet.
func (c *Conn) RecvPacket() (*protocol.Packet, error) {
	buf := make([]byte, protocol.PacketSize)
	if _, err := io.ReadFull(c.dec, buf); err != nil {
		return nil, fmt.Errorf("read packet: %w", err)
	}

	if err := protocol.ValidatePacket(buf, c.magic); err != nil {
		return nil, err
	}
	protocol.ClearStamp(buf)

	pt := protocol.PackageType(buf[0])
	if protocol.IsBigPacket(pt) {
		ext := make([]byte, protocol.PacketSize)
		if _, err := io.ReadFull(c.dec, ext); err != nil {
			return nil, fmt.Errorf("read extended: %w", err)
		}
		full := make([]byte, protocol.PacketSizeEx)
		copy(full, buf)
		copy(full[protocol.PacketSize:], ext)
		return protocol.UnmarshalPacket(full)
	}

	return protocol.UnmarshalPacket(buf)
}

// Close closes the connection.
func (c *Conn) Close() error {
	return c.raw.Close()
}
```

**Step 4: Run tests**

Run: `go test ./internal/network/ -v -run TestConnectionHandshake -timeout 10s`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/network/client.go internal/network/client_test.go
git commit -m "feat: TCP connection with encrypted handshake"
```

---

### Task 10: Packet Receiver and Input Handler

**Files:**
- Create: `internal/network/receiver.go`
- Create: `internal/network/handler.go`
- Create: `internal/network/handler_test.go`

**Step 1: Write the test**

```go
// internal/network/handler_test.go
package network

import (
	"testing"

	"github.com/bjk/mwb/internal/protocol"
)

// MockInputDevice records calls for testing.
type MockInputDevice struct {
	MouseMoves []struct{ X, Y int32 }
	ButtonDowns []uint16
	ButtonUps   []uint16
	Wheels      []int32
	KeyDowns    []uint16
	KeyUps      []uint16
}

func (m *MockInputDevice) MoveTo(x, y int32) error {
	m.MouseMoves = append(m.MouseMoves, struct{ X, Y int32 }{x, y})
	return nil
}
func (m *MockInputDevice) ButtonDown(btn uint16) error {
	m.ButtonDowns = append(m.ButtonDowns, btn)
	return nil
}
func (m *MockInputDevice) ButtonUp(btn uint16) error {
	m.ButtonUps = append(m.ButtonUps, btn)
	return nil
}
func (m *MockInputDevice) Wheel(delta int32) error {
	m.Wheels = append(m.Wheels, delta)
	return nil
}
func (m *MockInputDevice) KeyDown(code uint16) error {
	m.KeyDowns = append(m.KeyDowns, code)
	return nil
}
func (m *MockInputDevice) KeyUp(code uint16) error {
	m.KeyUps = append(m.KeyUps, code)
	return nil
}

func TestHandleMouseMove(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.X = 32768
	pkt.Mouse.Y = 16384
	pkt.Mouse.DwFlags = protocol.WM_MOUSEMOVE

	h.HandlePacket(pkt)

	if len(mock.MouseMoves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(mock.MouseMoves))
	}
	if mock.MouseMoves[0].X != 32768 || mock.MouseMoves[0].Y != 16384 {
		t.Errorf("move = (%d,%d), want (32768,16384)", mock.MouseMoves[0].X, mock.MouseMoves[0].Y)
	}
}

func TestHandleMouseButtons(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	// Left button down
	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_LBUTTONDOWN
	h.HandlePacket(pkt)

	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != 0x110 { // BTN_LEFT
		t.Errorf("expected BTN_LEFT down")
	}

	// Left button up
	pkt.Mouse.DwFlags = protocol.WM_LBUTTONUP
	h.HandlePacket(pkt)

	if len(mock.ButtonUps) != 1 || mock.ButtonUps[0] != 0x110 {
		t.Errorf("expected BTN_LEFT up")
	}
}

func TestHandleKeyboard(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	// Key down: VK_A (0x41)
	pkt := &protocol.Packet{Type: protocol.Keyboard}
	pkt.Keyboard.WVk = 0x41
	pkt.Keyboard.DwFlags = 0 // no UP flag = key down

	h.HandlePacket(pkt)

	if len(mock.KeyDowns) != 1 || mock.KeyDowns[0] != 30 { // KEY_A
		t.Errorf("expected KEY_A down, got %v", mock.KeyDowns)
	}

	// Key up: VK_A with LLKHF_UP
	pkt.Keyboard.DwFlags = protocol.LLKHF_UP
	h.HandlePacket(pkt)

	if len(mock.KeyUps) != 1 || mock.KeyUps[0] != 30 {
		t.Errorf("expected KEY_A up, got %v", mock.KeyUps)
	}
}

func TestHandleMouseWheel(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_MOUSEWHEEL
	pkt.Mouse.WheelDelta = 120 // Standard scroll up

	h.HandlePacket(pkt)

	if len(mock.Wheels) != 1 || mock.Wheels[0] != 1 {
		t.Errorf("expected wheel=1, got %v", mock.Wheels)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/network/ -v -run "TestHandle"`
Expected: FAIL

**Step 3: Write handler**

```go
// internal/network/handler.go
package network

import (
	"log/slog"

	"github.com/bjk/mwb/internal/input"
	"github.com/bjk/mwb/internal/protocol"
)

// MouseDevice is the interface for mouse injection.
type MouseDevice interface {
	MoveTo(x, y int32) error
	ButtonDown(button uint16) error
	ButtonUp(button uint16) error
	Wheel(delta int32) error
}

// KeyboardDevice is the interface for keyboard injection.
type KeyboardDevice interface {
	KeyDown(code uint16) error
	KeyUp(code uint16) error
}

// Handler processes incoming MWB packets and injects input events.
type Handler struct {
	Mouse    MouseDevice
	Keyboard KeyboardDevice
}

// HandlePacket dispatches a packet to the appropriate handler.
func (h *Handler) HandlePacket(pkt *protocol.Packet) {
	switch pkt.Type {
	case protocol.Mouse:
		h.handleMouse(pkt)
	case protocol.Keyboard:
		h.handleKeyboard(pkt)
	case protocol.Heartbeat, protocol.HeartbeatEx, protocol.HeartbeatExL2, protocol.HeartbeatExL3:
		// Heartbeats are handled at the receiver level
	default:
		slog.Debug("unhandled packet type", "type", pkt.Type)
	}
}

func (h *Handler) handleMouse(pkt *protocol.Packet) {
	md := pkt.Mouse

	switch int(md.DwFlags) {
	case protocol.WM_MOUSEMOVE:
		if err := h.Mouse.MoveTo(md.X, md.Y); err != nil {
			slog.Error("mouse move", "err", err)
		}

	case protocol.WM_LBUTTONDOWN:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonDown(input.BTN_LEFT)
	case protocol.WM_LBUTTONUP:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonUp(input.BTN_LEFT)

	case protocol.WM_RBUTTONDOWN:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonDown(input.BTN_RIGHT)
	case protocol.WM_RBUTTONUP:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonUp(input.BTN_RIGHT)

	case protocol.WM_MBUTTONDOWN:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonDown(input.BTN_MIDDLE)
	case protocol.WM_MBUTTONUP:
		h.Mouse.MoveTo(md.X, md.Y)
		h.Mouse.ButtonUp(input.BTN_MIDDLE)

	case protocol.WM_MOUSEWHEEL:
		// WheelDelta: 120 = one notch up, -120 = one notch down
		delta := md.WheelDelta / 120
		if delta == 0 && md.WheelDelta > 0 {
			delta = 1
		} else if delta == 0 && md.WheelDelta < 0 {
			delta = -1
		}
		h.Mouse.Wheel(delta)

	default:
		slog.Debug("unhandled mouse event", "flags", md.DwFlags)
	}
}

func (h *Handler) handleKeyboard(pkt *protocol.Packet) {
	kd := pkt.Keyboard
	evdevCode, ok := input.VKToEvdev(kd.WVk)
	if !ok {
		slog.Debug("unknown VK code", "vk", kd.WVk)
		return
	}

	isUp := (kd.DwFlags & protocol.LLKHF_UP) != 0

	var err error
	if isUp {
		err = h.Keyboard.KeyUp(evdevCode)
	} else {
		err = h.Keyboard.KeyDown(evdevCode)
	}
	if err != nil {
		slog.Error("keyboard inject", "err", err, "vk", kd.WVk, "up", isUp)
	}
}
```

**Step 4: Write receiver loop**

```go
// internal/network/receiver.go
package network

import (
	"errors"
	"io"
	"log/slog"

	"github.com/bjk/mwb/internal/protocol"
)

// ReceiveLoop reads packets from the connection and dispatches them.
// It returns when the connection is closed or an unrecoverable error occurs.
func ReceiveLoop(conn *Conn, handler *Handler) error {
	for {
		pkt, err := conn.RecvPacket()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				slog.Info("connection closed by remote")
				return nil
			}
			return err
		}

		switch pkt.Type {
		case protocol.Heartbeat, protocol.HeartbeatEx, protocol.HeartbeatExL2, protocol.HeartbeatExL3:
			slog.Debug("heartbeat received", "type", pkt.Type, "from", pkt.MachineName())
			// Respond with heartbeat
			resp := &protocol.Packet{
				Type: pkt.Type,
				Src:  conn.machineID,
				Des:  pkt.Src,
			}
			resp.SetMachineName(conn.RemoteName)
			if err := conn.SendPacket(resp); err != nil {
				slog.Error("send heartbeat response", "err", err)
			}

		case protocol.ByeBye:
			slog.Info("remote disconnected (ByeBye)")
			return nil

		case protocol.Invalid:
			slog.Warn("invalid packet received")

		case protocol.Handshake:
			slog.Debug("late handshake packet, ignoring")

		default:
			handler.HandlePacket(pkt)
		}
	}
}
```

**Step 5: Run tests**

Run: `go test ./internal/network/ -v -run "TestHandle"`
Expected: PASS

**Step 6: Commit**

```bash
git add internal/network/handler.go internal/network/handler_test.go internal/network/receiver.go
git commit -m "feat: packet handler with mouse/keyboard dispatch and receive loop"
```

---

### Task 11: Main Integration

**Files:**
- Modify: `cmd/mwb/main.go`

**Step 1: Write the full main.go**

```go
// cmd/mwb/main.go
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bjk/mwb/internal/config"
	"github.com/bjk/mwb/internal/input"
	"github.com/bjk/mwb/internal/network"
)

func main() {
	configPath := flag.String("config", "", "path to config.toml")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	// Configure logging
	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	// Find config file
	if *configPath == "" {
		home, _ := os.UserHomeDir()
		*configPath = filepath.Join(home, ".config", "mwb", "config.toml")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Create config at %s with:\n\n", *configPath)
		fmt.Fprintf(os.Stderr, "  host = \"192.168.1.100\"\n  key = \"YourSecurityKey\"\n  name = \"linux\"\n\n")
		os.Exit(1)
	}

	slog.Info("mwb starting", "host", cfg.Host, "port", cfg.MessagePort(), "name", cfg.Name)

	// Create virtual input devices
	mouse, err := input.CreateVirtualMouse("mwb-mouse")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual mouse: %v\n", err)
		fmt.Fprintf(os.Stderr, "Ensure your user is in the 'input' group: sudo usermod -aG input $USER\n")
		os.Exit(1)
	}
	defer mouse.Close()

	keyboard, err := input.CreateVirtualKeyboard("mwb-keyboard")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating virtual keyboard: %v\n", err)
		os.Exit(1)
	}
	defer keyboard.Close()

	slog.Info("virtual input devices created")

	handler := &network.Handler{
		Mouse:    mouse,
		Keyboard: keyboard,
	}

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Connection loop with exponential backoff
	go func() {
		backoff := 1 * time.Second
		maxBackoff := 30 * time.Second

		for {
			addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.MessagePort())
			slog.Info("connecting", "addr", addr)

			conn, err := network.Connect(addr, cfg.Key, cfg.Name, 10*time.Second)
			if err != nil {
				slog.Error("connection failed", "err", err, "retry_in", backoff)
				time.Sleep(backoff)
				backoff = min(backoff*2, maxBackoff)
				continue
			}

			slog.Info("connected", "remote", conn.RemoteName)
			backoff = 1 * time.Second // Reset on success

			if err := network.ReceiveLoop(conn, handler); err != nil {
				slog.Error("receive loop error", "err", err)
			}

			conn.Close()
			slog.Info("disconnected, will reconnect", "in", backoff)
			time.Sleep(backoff)
		}
	}()

	sig := <-sigCh
	slog.Info("shutting down", "signal", sig)
}
```

**Step 2: Verify it compiles**

Run: `cd /home/bjk/projects/scratch/mwb && go build -o mwb ./cmd/mwb`
Expected: Compiles successfully

**Step 3: Commit**

```bash
git add cmd/mwb/main.go
git commit -m "feat: main integration with reconnect loop and signal handling"
```

---

### Task 12: Run All Tests and Final Verification

**Step 1: Run all tests**

Run: `cd /home/bjk/projects/scratch/mwb && go test ./... -v`
Expected: All tests PASS

**Step 2: Verify binary builds**

Run: `go build -o mwb ./cmd/mwb && ls -la mwb`
Expected: Static binary produced

**Step 3: Run go vet**

Run: `go vet ./...`
Expected: No issues

**Step 4: Final commit**

```bash
git add -A && git commit -m "chore: verify all tests pass and binary builds"
```

---

## Testing Against Real Windows MWB

After all tasks are complete, test with a real Windows MWB host:

1. On Windows: Open PowerToys > Mouse Without Borders > Get security key
2. On Linux: Create `~/.config/mwb/config.toml`:
   ```toml
   host = "<windows-ip>"
   key = "<security-key-from-windows>"
   name = "linux"
   ```
3. Run: `./mwb -debug`
4. On Windows: Enter the Linux machine name in MWB settings
5. Move mouse to screen edge → should appear on Linux
