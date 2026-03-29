# macOS Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add macOS as a receive-only client platform, injecting mouse/keyboard events via CoreGraphics CGo bindings.

**Architecture:** Platform-specific code lives entirely in `internal/input/`. Linux uses uinput (existing), macOS uses CoreGraphics CGEventPost (new). The handler, network, protocol, and config layers are platform-independent and unchanged. Build tags (`//go:build linux` / `//go:build darwin`) gate compilation.

**Tech Stack:** Go, CGo (macOS only), Apple CoreGraphics framework

---

### Task 1: Extract shared button constants to `buttons.go`

The `BTN_LEFT/RIGHT/MIDDLE` constants currently live in `uinput.go` (linux-only). Move them to a shared file so the handler can reference them on all platforms.

**Files:**
- Create: `internal/input/buttons.go`
- Modify: `internal/input/uinput.go` (remove BTN_* constants)

**Step 1: Create `internal/input/buttons.go`**

```go
package input

// Button codes passed to MouseDevice.ButtonDown/ButtonUp.
// Values match Linux evdev codes for backward compatibility.
const (
	BTN_LEFT   uint16 = 0x110
	BTN_RIGHT  uint16 = 0x111
	BTN_MIDDLE uint16 = 0x112
)
```

**Step 2: Remove BTN_* from `internal/input/uinput.go`**

Delete lines 33-38 (the `// Buttons (exported for use by handler)` block with `BTN_LEFT`, `BTN_RIGHT`, `BTN_MIDDLE`).

**Step 3: Verify it compiles**

Run: `go build ./...`
Expected: success (no change in behavior, just moved constants)

**Step 4: Run tests**

Run: `go test ./...`
Expected: all pass

**Step 5: Commit**

```bash
git add internal/input/buttons.go internal/input/uinput.go
git commit -m "refactor: extract button constants to shared buttons.go"
```

---

### Task 2: Rename keymap to platform-specific + rename function

The current `keymap.go` references `KEY_*` constants from `uinput.go` (linux-only) so it implicitly only compiles on Linux. Make this explicit with a build tag and rename `VKToEvdev` to `VKToKeyCode` for platform-neutral naming.

**Files:**
- Rename: `internal/input/keymap.go` → `internal/input/keymap_linux.go`
- Rename: `internal/input/keymap_test.go` → `internal/input/keymap_linux_test.go`
- Modify: `internal/network/handler.go` (update function call)
- Modify: `internal/network/handler_test.go` (update expected values to be platform-independent)

**Step 1: Rename and add build tag to keymap**

```bash
git mv internal/input/keymap.go internal/input/keymap_linux.go
```

Add `//go:build linux` as the first line of `internal/input/keymap_linux.go`.

Rename the function at line 161:
```go
// VKToKeyCode maps a Windows Virtual Key code to a platform-specific key code.
func VKToKeyCode(vk int32) (uint16, bool) {
	code, ok := vkMap[vk]
	return code, ok
}
```

**Step 2: Rename and add build tag to keymap test**

```bash
git mv internal/input/keymap_test.go internal/input/keymap_linux_test.go
```

Add `//go:build linux` as the first line.

Update the test function name and all calls from `VKToEvdev` to `VKToKeyCode`:
- Line 6: rename `TestVKToEvdev` → `TestVKToKeyCode`
- Line 44: change `VKToEvdev(tt.vk)` → `VKToKeyCode(tt.vk)`
- Line 52: change `VKToEvdev(0x%X)` → `VKToKeyCode(0x%X)`

**Step 3: Update handler.go**

In `internal/network/handler.go` line 94, change:
```go
evdevCode, ok := input.VKToEvdev(kd.WVk)
```
to:
```go
keyCode, ok := input.VKToKeyCode(kd.WVk)
```

And update lines 101-103 to use `keyCode` instead of `evdevCode`:
```go
	if isUp {
		err = h.Keyboard.KeyUp(keyCode)
	} else {
		err = h.Keyboard.KeyDown(keyCode)
	}
```

**Step 4: Update handler_test.go for platform independence**

In `internal/network/handler_test.go`, the keyboard test hardcodes Linux-specific keycode `30` for KEY_A. Make it platform-independent by using `input.VKToKeyCode`.

Add import: `"github.com/lucky-verma/mwb-linux/internal/input"`

Update `TestHandleKeyboard` (lines 82-102):
```go
func TestHandleKeyboard(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	expectedCode, ok := input.VKToKeyCode(0x41)
	if !ok {
		t.Fatal("VKToKeyCode(0x41) should map VK_A")
	}

	// Key down: VK_A (0x41)
	pkt := &protocol.Packet{Type: protocol.Keyboard}
	pkt.Keyboard.WVk = 0x41
	pkt.Keyboard.DwFlags = 0

	h.HandlePacket(pkt)
	if len(mock.KeyDowns) != 1 || mock.KeyDowns[0] != expectedCode {
		t.Errorf("expected keycode %d down, got %v", expectedCode, mock.KeyDowns)
	}

	// Key up: VK_A with LLKHF_UP (0x80)
	pkt.Keyboard.DwFlags = protocol.LLKHF_UP
	h.HandlePacket(pkt)
	if len(mock.KeyUps) != 1 || mock.KeyUps[0] != expectedCode {
		t.Errorf("expected keycode %d up, got %v", expectedCode, mock.KeyUps)
	}
}
```

Also update button tests to use `input.BTN_LEFT` / `input.BTN_RIGHT` / `input.BTN_MIDDLE` instead of hardcoded `0x110`, `0x111`, `0x112`.

**Step 5: Verify it compiles and tests pass**

Run: `go test ./...`
Expected: all pass

**Step 6: Commit**

```bash
git add internal/input/keymap_linux.go internal/input/keymap_linux_test.go \
  internal/network/handler.go internal/network/handler_test.go
git commit -m "refactor: rename VKToEvdev to VKToKeyCode, add linux build tags to keymap"
```

---

### Task 3: Create macOS keymap

Create the Windows VK → macOS virtual keycode mapping, mirroring the Linux keymap structure.

**Files:**
- Create: `internal/input/keymap_darwin.go`
- Create: `internal/input/keymap_darwin_test.go`

**Step 1: Write the test first**

Create `internal/input/keymap_darwin_test.go`:

```go
//go:build darwin

package input

import "testing"

func TestVKToKeyCode(t *testing.T) {
	tests := []struct {
		name string
		vk   int32
		want uint16
	}{
		{"VK_A", 0x41, 0x00},       // kVK_ANSI_A
		{"VK_Z", 0x5A, 0x06},       // kVK_ANSI_Z
		{"VK_0", 0x30, 0x1D},       // kVK_ANSI_0
		{"VK_9", 0x39, 0x19},       // kVK_ANSI_9
		{"VK_RETURN", 0x0D, 0x24},  // kVK_Return
		{"VK_ESCAPE", 0x1B, 0x35},  // kVK_Escape
		{"VK_SPACE", 0x20, 0x31},   // kVK_Space
		{"VK_TAB", 0x09, 0x30},     // kVK_Tab
		{"VK_BACK", 0x08, 0x33},    // kVK_Delete (backspace)
		{"VK_LSHIFT", 0xA0, 0x38},  // kVK_Shift
		{"VK_RSHIFT", 0xA1, 0x3C},  // kVK_RightShift
		{"VK_LCONTROL", 0xA2, 0x3B}, // kVK_Control
		{"VK_RCONTROL", 0xA3, 0x3E}, // kVK_RightControl
		{"VK_LMENU", 0xA4, 0x3A},   // kVK_Option
		{"VK_RMENU", 0xA5, 0x3D},   // kVK_RightOption
		{"VK_LWIN", 0x5B, 0x37},    // kVK_Command
		{"VK_F1", 0x70, 0x7A},      // kVK_F1
		{"VK_F12", 0x7B, 0x6F},     // kVK_F12
		{"VK_LEFT", 0x25, 0x7B},    // kVK_LeftArrow
		{"VK_UP", 0x26, 0x7E},      // kVK_UpArrow
		{"VK_RIGHT", 0x27, 0x7C},   // kVK_RightArrow
		{"VK_DOWN", 0x28, 0x7D},    // kVK_DownArrow
		{"VK_DELETE", 0x2E, 0x75},  // kVK_ForwardDelete
		{"VK_HOME", 0x24, 0x73},    // kVK_Home
		{"VK_END", 0x23, 0x77},     // kVK_End
		{"VK_PRIOR", 0x21, 0x74},   // kVK_PageUp
		{"VK_NEXT", 0x22, 0x79},    // kVK_PageDown
		{"unknown", 0xFFF, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := VKToKeyCode(tt.vk)
			if tt.want == 0 && tt.vk == 0xFFF {
				if ok {
					t.Errorf("expected unknown for VK 0x%X", tt.vk)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("VKToKeyCode(0x%X) = 0x%X, %v; want 0x%X, true", tt.vk, got, ok, tt.want)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/input/ -run TestVKToKeyCode -v`
Expected: FAIL — `VKToKeyCode` not defined (on darwin)

**Step 3: Create `internal/input/keymap_darwin.go`**

```go
//go:build darwin

package input

// macOS virtual key codes (from Events.h / Carbon HIToolbox)
const (
	kVK_ANSI_A         uint16 = 0x00
	kVK_ANSI_S         uint16 = 0x01
	kVK_ANSI_D         uint16 = 0x02
	kVK_ANSI_F         uint16 = 0x03
	kVK_ANSI_H         uint16 = 0x04
	kVK_ANSI_G         uint16 = 0x05
	kVK_ANSI_Z         uint16 = 0x06
	kVK_ANSI_X         uint16 = 0x07
	kVK_ANSI_C         uint16 = 0x08
	kVK_ANSI_V         uint16 = 0x09
	kVK_ANSI_B         uint16 = 0x0B
	kVK_ANSI_Q         uint16 = 0x0C
	kVK_ANSI_W         uint16 = 0x0D
	kVK_ANSI_E         uint16 = 0x0E
	kVK_ANSI_R         uint16 = 0x0F
	kVK_ANSI_Y         uint16 = 0x10
	kVK_ANSI_T         uint16 = 0x11
	kVK_ANSI_1         uint16 = 0x12
	kVK_ANSI_2         uint16 = 0x13
	kVK_ANSI_3         uint16 = 0x14
	kVK_ANSI_4         uint16 = 0x15
	kVK_ANSI_6         uint16 = 0x16
	kVK_ANSI_5         uint16 = 0x17
	kVK_ANSI_Equal     uint16 = 0x18
	kVK_ANSI_9         uint16 = 0x19
	kVK_ANSI_7         uint16 = 0x1A
	kVK_ANSI_Minus     uint16 = 0x1B
	kVK_ANSI_8         uint16 = 0x1C
	kVK_ANSI_0         uint16 = 0x1D
	kVK_ANSI_RBracket  uint16 = 0x1E
	kVK_ANSI_O         uint16 = 0x1F
	kVK_ANSI_U         uint16 = 0x20
	kVK_ANSI_LBracket  uint16 = 0x21
	kVK_ANSI_I         uint16 = 0x22
	kVK_ANSI_P         uint16 = 0x23
	kVK_Return         uint16 = 0x24
	kVK_ANSI_L         uint16 = 0x25
	kVK_ANSI_J         uint16 = 0x26
	kVK_ANSI_Quote     uint16 = 0x27
	kVK_ANSI_K         uint16 = 0x28
	kVK_ANSI_Semicolon uint16 = 0x29
	kVK_ANSI_Backslash uint16 = 0x2A
	kVK_ANSI_Comma     uint16 = 0x2B
	kVK_ANSI_Slash     uint16 = 0x2C
	kVK_ANSI_N         uint16 = 0x2D
	kVK_ANSI_M         uint16 = 0x2E
	kVK_ANSI_Period    uint16 = 0x2F
	kVK_Tab            uint16 = 0x30
	kVK_Space          uint16 = 0x31
	kVK_ANSI_Grave     uint16 = 0x32
	kVK_Delete         uint16 = 0x33 // backspace
	kVK_Escape         uint16 = 0x35
	kVK_Command        uint16 = 0x37
	kVK_Shift          uint16 = 0x38
	kVK_CapsLock       uint16 = 0x39
	kVK_Option         uint16 = 0x3A
	kVK_Control        uint16 = 0x3B
	kVK_RightShift     uint16 = 0x3C
	kVK_RightOption    uint16 = 0x3D
	kVK_RightControl   uint16 = 0x3E

	kVK_ANSI_KpDecimal  uint16 = 0x41
	kVK_ANSI_KpMultiply uint16 = 0x43
	kVK_ANSI_KpPlus     uint16 = 0x45
	kVK_ANSI_KpClear    uint16 = 0x47
	kVK_VolumeUp        uint16 = 0x48
	kVK_VolumeDown      uint16 = 0x49
	kVK_Mute            uint16 = 0x4A
	kVK_ANSI_KpDivide   uint16 = 0x4B
	kVK_ANSI_KpEnter    uint16 = 0x4C
	kVK_ANSI_KpMinus    uint16 = 0x4E
	kVK_ANSI_Keypad0    uint16 = 0x52
	kVK_ANSI_Keypad1    uint16 = 0x53
	kVK_ANSI_Keypad2    uint16 = 0x54
	kVK_ANSI_Keypad3    uint16 = 0x55
	kVK_ANSI_Keypad4    uint16 = 0x56
	kVK_ANSI_Keypad5    uint16 = 0x57
	kVK_ANSI_Keypad6    uint16 = 0x58
	kVK_ANSI_Keypad7    uint16 = 0x59
	kVK_ANSI_Keypad8    uint16 = 0x5A
	kVK_ANSI_Keypad9    uint16 = 0x5B

	kVK_F1            uint16 = 0x7A
	kVK_F2            uint16 = 0x78
	kVK_F3            uint16 = 0x63 // Note: Apple's F3 is 0x63, not sequential
	kVK_F4            uint16 = 0x76
	kVK_F5            uint16 = 0x60
	kVK_F6            uint16 = 0x61
	kVK_F7            uint16 = 0x62
	kVK_F8            uint16 = 0x64 // Note: Apple skips 0x63 (F3)
	kVK_F9            uint16 = 0x65
	kVK_F10           uint16 = 0x6D
	kVK_F11           uint16 = 0x67
	kVK_F12           uint16 = 0x6F
	kVK_Help          uint16 = 0x72
	kVK_Home          uint16 = 0x73
	kVK_PageUp        uint16 = 0x74
	kVK_ForwardDelete uint16 = 0x75
	kVK_End           uint16 = 0x77
	kVK_PageDown      uint16 = 0x79
	kVK_LeftArrow     uint16 = 0x7B
	kVK_RightArrow    uint16 = 0x7C
	kVK_DownArrow     uint16 = 0x7D
	kVK_UpArrow       uint16 = 0x7E
)

// vkMap maps Windows VK codes to macOS virtual key codes.
var vkMap = map[int32]uint16{
	0x08: kVK_Delete, 0x09: kVK_Tab, 0x0D: kVK_Return,
	0x14: kVK_CapsLock, 0x1B: kVK_Escape, 0x20: kVK_Space,
	0x21: kVK_PageUp, 0x22: kVK_PageDown, 0x23: kVK_End, 0x24: kVK_Home,
	0x25: kVK_LeftArrow, 0x26: kVK_UpArrow, 0x27: kVK_RightArrow, 0x28: kVK_DownArrow,
	0x2D: kVK_Help, 0x2E: kVK_ForwardDelete,
	0x5B: kVK_Command, 0x5C: kVK_Command, // both Win keys map to Command
	0x60: kVK_ANSI_Keypad0, 0x61: kVK_ANSI_Keypad1, 0x62: kVK_ANSI_Keypad2, 0x63: kVK_ANSI_Keypad3,
	0x64: kVK_ANSI_Keypad4, 0x65: kVK_ANSI_Keypad5, 0x66: kVK_ANSI_Keypad6, 0x67: kVK_ANSI_Keypad7,
	0x68: kVK_ANSI_Keypad8, 0x69: kVK_ANSI_Keypad9,
	0x6A: kVK_ANSI_KpMultiply, 0x6B: kVK_ANSI_KpPlus, 0x6D: kVK_ANSI_KpMinus,
	0x6E: kVK_ANSI_KpDecimal, 0x6F: kVK_ANSI_KpDivide,
	0x70: kVK_F1, 0x71: kVK_F2, 0x72: kVK_F3, 0x73: kVK_F4,
	0x74: kVK_F5, 0x75: kVK_F6, 0x76: kVK_F7, 0x77: kVK_F8,
	0x78: kVK_F9, 0x79: kVK_F10, 0x7A: kVK_F11, 0x7B: kVK_F12,
	0x90: kVK_ANSI_KpClear, // NumLock maps to Clear on macOS
	0xA0: kVK_Shift, 0xA1: kVK_RightShift,
	0xA2: kVK_Control, 0xA3: kVK_RightControl,
	0xA4: kVK_Option, 0xA5: kVK_RightOption,
	0xAD: kVK_Mute, 0xAE: kVK_VolumeDown, 0xAF: kVK_VolumeUp,
	0xBA: kVK_ANSI_Semicolon, 0xBB: kVK_ANSI_Equal, 0xBC: kVK_ANSI_Comma,
	0xBD: kVK_ANSI_Minus, 0xBE: kVK_ANSI_Period, 0xBF: kVK_ANSI_Slash,
	0xC0: kVK_ANSI_Grave, 0xDB: kVK_ANSI_LBracket, 0xDC: kVK_ANSI_Backslash,
	0xDD: kVK_ANSI_RBracket, 0xDE: kVK_ANSI_Quote,
}

func init() {
	// A-Z: VK 0x41-0x5A → macOS keycodes (not sequential!)
	letters := []uint16{
		kVK_ANSI_A, kVK_ANSI_B, kVK_ANSI_C, kVK_ANSI_D, kVK_ANSI_E, kVK_ANSI_F,
		kVK_ANSI_G, kVK_ANSI_H, kVK_ANSI_I, kVK_ANSI_J, kVK_ANSI_K, kVK_ANSI_L,
		kVK_ANSI_M, kVK_ANSI_N, kVK_ANSI_O, kVK_ANSI_P, kVK_ANSI_Q, kVK_ANSI_R,
		kVK_ANSI_S, kVK_ANSI_T, kVK_ANSI_U, kVK_ANSI_V, kVK_ANSI_W, kVK_ANSI_X,
		kVK_ANSI_Y, kVK_ANSI_Z,
	}
	for vk := int32(0x41); vk <= 0x5A; vk++ {
		vkMap[vk] = letters[vk-0x41]
	}
	// 0-9: VK 0x30-0x39 → macOS keycodes (not sequential!)
	digits := []uint16{
		kVK_ANSI_0, kVK_ANSI_1, kVK_ANSI_2, kVK_ANSI_3, kVK_ANSI_4,
		kVK_ANSI_5, kVK_ANSI_6, kVK_ANSI_7, kVK_ANSI_8, kVK_ANSI_9,
	}
	for vk := int32(0x30); vk <= 0x39; vk++ {
		vkMap[vk] = digits[vk-0x30]
	}
}

// VKToKeyCode maps a Windows Virtual Key code to a macOS virtual key code.
func VKToKeyCode(vk int32) (uint16, bool) {
	code, ok := vkMap[vk]
	return code, ok
}
```

**Step 4: Run tests**

Run: `go test ./internal/input/ -run TestVKToKeyCode -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/input/keymap_darwin.go internal/input/keymap_darwin_test.go
git commit -m "feat: add macOS virtual keycode mapping"
```

---

### Task 4: Create macOS CoreGraphics input driver

Implement `VirtualMouse` and `VirtualKeyboard` using CGo calls to Apple's CoreGraphics framework.

**Files:**
- Create: `internal/input/coregraphics.go`

**Step 1: Create `internal/input/coregraphics.go`**

```go
//go:build darwin

package input

/*
#cgo LDFLAGS: -framework CoreGraphics

#include <CoreGraphics/CoreGraphics.h>

// postMouseEvent creates and posts a mouse event.
static int postMouseEvent(CGEventType type, CGFloat x, CGFloat y, CGMouseButton button) {
	CGPoint point = CGPointMake(x, y);
	CGEventRef event = CGEventCreateMouseEvent(NULL, type, point, button);
	if (event == NULL) return -1;
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
	return 0;
}

// postKeyEvent creates and posts a keyboard event.
static int postKeyEvent(CGKeyCode keycode, int down) {
	CGEventRef event = CGEventCreateKeyboardEvent(NULL, keycode, down ? true : false);
	if (event == NULL) return -1;
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
	return 0;
}

// postScrollEvent creates and posts a scroll wheel event.
static int postScrollEvent(int32_t delta) {
	CGEventRef event = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 1, delta);
	if (event == NULL) return -1;
	CGEventPost(kCGHIDEventTap, event);
	CFRelease(event);
	return 0;
}

// getMainDisplayWidth returns the width of the main display in pixels.
static size_t getMainDisplayWidth(void) {
	return CGDisplayPixelsWide(CGMainDisplayID());
}

// getMainDisplayHeight returns the height of the main display in pixels.
static size_t getMainDisplayHeight(void) {
	return CGDisplayPixelsHigh(CGMainDisplayID());
}
*/
import "C"

import (
	"fmt"
)

// VirtualMouse injects mouse events via CoreGraphics.
type VirtualMouse struct {
	screenW float64
	screenH float64
	lastX   float64
	lastY   float64
}

// CreateVirtualMouse creates a virtual mouse that posts CoreGraphics events.
// The name parameter is accepted for API compatibility but unused on macOS.
func CreateVirtualMouse(name string) (*VirtualMouse, error) {
	w := float64(C.getMainDisplayWidth())
	h := float64(C.getMainDisplayHeight())
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("could not get display dimensions\nEnsure Accessibility permission is granted:\n  System Settings > Privacy & Security > Accessibility")
	}
	return &VirtualMouse{screenW: w, screenH: h}, nil
}

// MoveTo moves the cursor to absolute position (x, y) in range 0-65535.
func (m *VirtualMouse) MoveTo(x, y int32) error {
	px := float64(x) / 65535.0 * m.screenW
	py := float64(y) / 65535.0 * m.screenH
	m.lastX = px
	m.lastY = py
	if C.postMouseEvent(C.kCGEventMouseMoved, C.CGFloat(px), C.CGFloat(py), 0) != 0 {
		return fmt.Errorf("failed to post mouse move event")
	}
	return nil
}

func (m *VirtualMouse) buttonEvent(button uint16, down bool) error {
	var eventDown, eventUp C.CGEventType
	var cgButton C.CGMouseButton

	switch button {
	case BTN_LEFT:
		eventDown = C.kCGEventLeftMouseDown
		eventUp = C.kCGEventLeftMouseUp
		cgButton = C.kCGMouseButtonLeft
	case BTN_RIGHT:
		eventDown = C.kCGEventRightMouseDown
		eventUp = C.kCGEventRightMouseUp
		cgButton = C.kCGMouseButtonRight
	case BTN_MIDDLE:
		eventDown = C.kCGEventOtherMouseDown
		eventUp = C.kCGEventOtherMouseUp
		cgButton = C.kCGMouseButtonCenter
	default:
		return fmt.Errorf("unknown button: 0x%X", button)
	}

	eventType := eventDown
	if !down {
		eventType = eventUp
	}

	if C.postMouseEvent(eventType, C.CGFloat(m.lastX), C.CGFloat(m.lastY), cgButton) != 0 {
		return fmt.Errorf("failed to post mouse button event")
	}
	return nil
}

// ButtonDown presses a mouse button.
func (m *VirtualMouse) ButtonDown(button uint16) error {
	return m.buttonEvent(button, true)
}

// ButtonUp releases a mouse button.
func (m *VirtualMouse) ButtonUp(button uint16) error {
	return m.buttonEvent(button, false)
}

// Wheel scrolls the mouse wheel by delta units (positive = up, negative = down).
func (m *VirtualMouse) Wheel(delta int32) error {
	if C.postScrollEvent(C.int32_t(delta)) != 0 {
		return fmt.Errorf("failed to post scroll event")
	}
	return nil
}

// Close is a no-op on macOS (no kernel resource to release).
func (m *VirtualMouse) Close() error {
	return nil
}

// VirtualKeyboard injects keyboard events via CoreGraphics.
type VirtualKeyboard struct{}

// CreateVirtualKeyboard creates a virtual keyboard that posts CoreGraphics events.
// The name parameter is accepted for API compatibility but unused on macOS.
func CreateVirtualKeyboard(name string) (*VirtualKeyboard, error) {
	return &VirtualKeyboard{}, nil
}

// KeyDown presses a key identified by its macOS virtual keycode.
func (k *VirtualKeyboard) KeyDown(code uint16) error {
	if C.postKeyEvent(C.CGKeyCode(code), 1) != 0 {
		return fmt.Errorf("failed to post key down event")
	}
	return nil
}

// KeyUp releases a key identified by its macOS virtual keycode.
func (k *VirtualKeyboard) KeyUp(code uint16) error {
	if C.postKeyEvent(C.CGKeyCode(code), 0) != 0 {
		return fmt.Errorf("failed to post key up event")
	}
	return nil
}

// Close is a no-op on macOS.
func (k *VirtualKeyboard) Close() error {
	return nil
}
```

**Step 2: Verify it compiles**

Run: `go build ./...`
Expected: success on macOS (CGo compiles the CoreGraphics bindings)

**Step 3: Commit**

```bash
git add internal/input/coregraphics.go
git commit -m "feat: add macOS CoreGraphics input driver"
```

---

### Task 5: Update goreleaser for macOS builds

Add darwin targets to the release configuration. macOS needs CGO_ENABLED=1 while Linux stays at 0, so we need separate build entries.

**Files:**
- Modify: `.goreleaser.yml`

**Step 1: Update `.goreleaser.yml`**

Split the single build into two — one for Linux (CGO disabled) and one for macOS (CGO enabled):

```yaml
version: 2

builds:
  - id: mwb-linux
    main: ./cmd/mwb
    binary: mwb
    env:
      - CGO_ENABLED=0
    goos:
      - linux
    goarch:
      - amd64
      - arm64

  - id: mwb-darwin
    main: ./cmd/mwb
    binary: mwb
    env:
      - CGO_ENABLED=1
    goos:
      - darwin
    goarch:
      - amd64
      - arm64

archives:
  - formats:
      - tar.gz
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

nfpms:
  - id: mwb
    package_name: mwb
    file_name_template: "{{ .ConventionalFileName }}"
    ids:
      - mwb-linux
    vendor: Brian Ketelsen
    homepage: https://github.com/lucky-verma/mwb-linux
    maintainer: Brian Ketelsen <mail@bjk.dev>
    description: Mouse Without Borders Linux client
    license: MIT
    formats:
      - deb
      - rpm
    bindir: /usr/bin
    contents:
      - src: packaging/mwb.service
        dst: /usr/lib/systemd/user/mwb.service
        type: config
      - src: packaging/99-mwb.rules
        dst: /usr/lib/udev/rules.d/99-mwb.rules
        type: config
    scripts:
      postinstall: packaging/scripts/postinstall.sh
      preremove: packaging/scripts/preremove.sh

checksum:
  name_template: "checksums.txt"

changelog:
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^test:"
      - "^chore:"
```

Note: The nfpms section only references `mwb-linux` since deb/rpm packages are Linux-only. macOS users install from the tar.gz archive.

**Step 2: Verify goreleaser config**

Run: `goreleaser check` (if installed) or just verify the YAML is valid.

**Step 3: Commit**

```bash
git add .goreleaser.yml
git commit -m "build: add macOS darwin targets to goreleaser"
```

---

### Task 6: Add macOS CI testing

Add a macOS runner to the test workflow so keymap and build are validated on darwin.

**Files:**
- Modify: `.github/workflows/test.yml`

**Step 1: Update test workflow**

Add a matrix strategy to run on both ubuntu and macos:

```yaml
name: Test

on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: stable

      - name: Test
        run: go test -race ./...

      - name: Vet
        run: go vet ./...

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: stable

      - name: Lint
        uses: golangci/golangci-lint-action@v7
        with:
          version: v2.10.1
```

Note: Lint stays ubuntu-only (golangci-lint works cross-platform via Go analysis, no need to duplicate). Tests run on both to catch platform-specific compilation issues.

**Step 2: Commit**

```bash
git add .github/workflows/test.yml
git commit -m "ci: add macOS test runner"
```

---

### Task 7: Update project documentation

Update CLAUDE.md to reflect the multi-platform structure.

**Files:**
- Modify: `CLAUDE.md`

**Step 1: Update CLAUDE.md**

Add macOS information to the System Requirements and Project Structure sections.

In Project Structure, update the `input/` description:
```
  input/            - platform-specific virtual mouse/keyboard devices
                      uinput.go (Linux), coregraphics.go (macOS)
```

Add macOS System Requirements section:
```
## macOS System Requirements

Accessibility permission must be granted:
1. Open System Settings > Privacy & Security > Accessibility
2. Add and enable the `mwb` binary (or Terminal.app if running from terminal)
3. Build requires CGo: `CGO_ENABLED=1 go build ./cmd/mwb`
```

**Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: add macOS build and permission info to CLAUDE.md"
```

---

### Task 8: Final verification

Run the full check suite to make sure everything works.

**Step 1: Run `make check`**

Run: `make check`
Expected: fmt, lint, and test all pass

**Step 2: Verify macOS build**

Run: `go build -o mwb ./cmd/mwb`
Expected: binary builds successfully with CGo

**Step 3: Manual smoke test (optional)**

Run: `./mwb -config ~/.config/mwb/config.toml -debug`
Expected: connects to Windows MWB host, receives and injects mouse/keyboard events
