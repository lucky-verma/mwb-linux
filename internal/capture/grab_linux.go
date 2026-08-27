//go:build linux

package capture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// EVIOCGRAB (_IOW('E', 0x90, int)) takes an exclusive kernel grab on an evdev
// device. While held, events reach ONLY the grabbing file descriptor, so X11,
// Wayland and the console see nothing from that device.
//
// The grab is owned by the FILE DESCRIPTOR, which is the entire reason it is
// used here. The kernel drops the grab when the fd closes — on clean exit, and
// equally on crash, SIGKILL or OOM. Local input therefore cannot be left
// suppressed by a release path that failed to run.
//
// This replaced `xinput disable`, whose effect was global X11 state that
// outlived the process: when a matching `xinput enable` never ran, the machine
// was left with no mouse and no keyboard and nothing in the system would
// restore them.
const eviocgrab uintptr = 0x40044590

// Additional evdev codes for capability probing. Event types (evKey, evRel) and
// axis codes (relX, relY) are already declared in capture_linux.go.
const (
	evAbsType uint = 0x03

	absX uint = 0x00
	absY uint = 0x01

	btnMouse      uint = 0x110 // BTN_LEFT
	btnToolPen    uint = 0x140
	btnToolFinger uint = 0x145
	btnTouch      uint = 0x14a
	btnStylus     uint = 0x14b

	keyMax uint = 0x2ff
	relMax uint = 0x0f
	absMax uint = 0x3f
)

// eviocgbit builds EVIOCGBIT(evType, length) = _IOC(_IOC_READ, 'E', 0x20+evType, length),
// which reads a device's capability bitmask for one event type.
func eviocgbit(evType, length uintptr) uintptr {
	const iocRead = 2
	return (iocRead << 30) | (length << 16) | ('E' << 8) | (0x20 + evType)
}

// testBit reports whether bit n is set in a little-endian capability bitmask.
func testBit(bits []byte, n uint) bool {
	idx := n / 8
	if idx >= uint(len(bits)) {
		return false
	}
	return bits[idx]&(1<<(n%8)) != 0
}

// hasRelativePointerAxes identifies the motion node of a mouse or trackball.
// Some USB receivers expose movement and buttons on different evdev nodes, so
// requiring BTN_MOUSE on the movement node drops real pointer motion.
func hasRelativePointerAxes(relBits []byte) bool {
	return testBit(relBits, relX) && testBit(relBits, relY)
}

// capabilityBits reads the capability bitmask for one event type off an open
// evdev fd. A zeroed slice is returned when the device reports nothing.
func capabilityBits(f *os.File, evType uint, maxCode uint) ([]byte, error) {
	buf := make([]byte, maxCode/8+1)
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		eviocgbit(uintptr(evType), uintptr(len(buf))),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if errno != 0 {
		return nil, fmt.Errorf("EVIOCGBIT(%d): %w", evType, errno)
	}
	return buf, nil
}

// isVirtualDevice reports whether a device node is backed by uinput rather than
// by real hardware. Virtual devices live under /sys/devices/virtual/input, real
// ones under their bus path (pci…, usb…, platform…).
//
// These must never be grabbed. mwb injects remote input through its own uinput
// devices, so grabbing them would swallow the very events mwb is replaying —
// remote typing and clicks would silently stop working. The same reasoning
// protects other users' tools (input-remapper, ydotool, other KVM software),
// whose output devices would break the same way.
//
// On error the device is treated as virtual and skipped. Failing closed costs a
// little local input leaking to X; failing open risks an input feedback loop,
// which is far harder to diagnose.
func isVirtualDevice(devPath string) bool {
	target, err := filepath.EvalSymlinks(filepath.Join("/sys/class/input", filepath.Base(devPath)))
	if err != nil {
		return true
	}
	return strings.Contains(target, "/devices/virtual/")
}

// isGrabTarget reports whether a device is a keyboard or a pointer, and so
// should be suppressed while the cursor is on the remote machine.
//
// Classification mirrors udev's own input_id builtin and reads only capability
// bitmasks, never vendor or product names, so it behaves the same on any
// hardware. Three device classes qualify:
//
//	relative pointers  mice, trackballs
//	absolute pointers  touchpads, touchscreens, graphics tablets
//	full keyboards     anything exposing the whole base key range
//
// Everything else is deliberately left alone: power and sleep buttons, lid
// switches, audio-jack detection, brightness and media keys, webcam hotkeys.
// Grabbing those would swallow local hardware functions that mwb does not
// forward anyway — and taking the power button would remove the last-resort
// recovery path.
func isGrabTarget(f *os.File) bool {
	if isVirtualDevice(f.Name()) {
		return false
	}

	keyBits, err := capabilityBits(f, evKey, keyMax)
	if err != nil {
		return false
	}

	// Relative pointer: both motion axes are sufficient. Unlike absolute X/Y,
	// relative X/Y are not used by ordinary joysticks or gamepads. Do not require
	// BTN_MOUSE here: split-interface mice can expose buttons on another node.
	if relBits, err := capabilityBits(f, evRel, relMax); err == nil {
		if hasRelativePointerAxes(relBits) {
			return true
		}
	}

	// Absolute pointer: ABS_X + ABS_Y plus a touch or tool button. The button
	// requirement is what separates touchpads, touchscreens and tablets from
	// joysticks, gamepads and accelerometers, which also report absolute axes
	// but must not be grabbed.
	if absBits, err := capabilityBits(f, evAbsType, absMax); err == nil {
		if testBit(absBits, absX) && testBit(absBits, absY) {
			for _, btn := range []uint{btnTouch, btnToolFinger, btnStylus, btnToolPen, btnMouse} {
				if testBit(keyBits, btn) {
					return true
				}
			}
		}
	}

	// Keyboard: udev treats a device as a full keyboard when every key in the
	// first 32 codes is present — KEY_ESC, the number row, and Q through S —
	// skipping KEY_RESERVED at bit 0.
	for bit := uint(1); bit < 32; bit++ {
		if !testBit(keyBits, bit) {
			return false
		}
	}
	return true
}

// setGrab acquires or releases the exclusive grab on one device.
//
// Releasing a device that is not currently grabbed returns EINVAL; that is the
// already-released case and is reported as success, so release stays idempotent.
func setGrab(f *os.File, grab bool) error {
	var arg uintptr
	if grab {
		arg = 1
	}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, arg)
	if errno == 0 {
		return nil
	}
	if !grab && errno == syscall.EINVAL {
		return nil
	}
	return errno
}
