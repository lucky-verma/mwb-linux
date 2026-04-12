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

// Relative axes
const (
	relWheel  uint16 = 0x08
	relHWheel uint16 = 0x06 // horizontal scroll
)

// Absolute axes
const (
	absX   uint16 = 0x00
	absY   uint16 = 0x01
	absCnt        = 0x40
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
		return nil, fmt.Errorf("open /dev/uinput: %w\nSetup required:\n  1. sudo modprobe uinput\n  2. echo 'uinput' | sudo tee /etc/modules-load.d/uinput.conf\n  3. echo 'KERNEL==\"uinput\", GROUP=\"input\", MODE=\"0660\"' | sudo tee /etc/udev/rules.d/99-uinput.rules\n  4. sudo udevadm control --reload-rules && sudo udevadm trigger /dev/uinput\n  5. Ensure your user is in the 'input' group: sudo usermod -aG input $USER", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = fd.Close()
		}
	}()

	for _, ev := range []uintptr{uintptr(evKey), uintptr(evAbs), uintptr(evRel)} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
		}
	}

	for _, btn := range []uintptr{
		uintptr(BTN_LEFT), uintptr(BTN_RIGHT), uintptr(BTN_MIDDLE),
		uintptr(BTN_SIDE), uintptr(BTN_EXTRA), // X-button 1 and 2
	} {
		if err := ioctl(fd, uiSetKeyBit, btn); err != nil {
			return nil, fmt.Errorf("UI_SET_KEYBIT: %w", err)
		}
	}

	for _, axis := range []uintptr{uintptr(absX), uintptr(absY)} {
		if err := ioctl(fd, uiSetAbsBit, axis); err != nil {
			return nil, fmt.Errorf("UI_SET_ABSBIT: %w", err)
		}
	}

	for _, rel := range []uintptr{uintptr(relWheel), uintptr(relHWheel)} {
		if err := ioctl(fd, uiSetRelBit, rel); err != nil {
			return nil, fmt.Errorf("UI_SET_RELBIT: %w", err)
		}
	}

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

// MoveTo moves the virtual mouse cursor to absolute position (x, y) in range 0-65535.
func (m *VirtualMouse) MoveTo(x, y int32) error {
	if err := writeEvent(m.fd, evAbs, absX, x); err != nil {
		return err
	}
	if err := writeEvent(m.fd, evAbs, absY, y); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

// ButtonDown presses a mouse button (BTN_LEFT, BTN_RIGHT, or BTN_MIDDLE).
func (m *VirtualMouse) ButtonDown(button uint16) error {
	if err := writeEvent(m.fd, evKey, button, 1); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

// ButtonUp releases a mouse button (BTN_LEFT, BTN_RIGHT, or BTN_MIDDLE).
func (m *VirtualMouse) ButtonUp(button uint16) error {
	if err := writeEvent(m.fd, evKey, button, 0); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

// Wheel scrolls the mouse wheel by delta units (positive = up, negative = down).
func (m *VirtualMouse) Wheel(delta int32) error {
	if err := writeEvent(m.fd, evRel, relWheel, delta); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

func (m *VirtualMouse) HWheel(delta int32) error {
	if err := writeEvent(m.fd, evRel, relHWheel, delta); err != nil {
		return err
	}
	return syncEvents(m.fd)
}

// Close destroys the virtual device and closes the file descriptor.
func (m *VirtualMouse) Close() error {
	_ = ioctl(m.fd, uiDevDestroy, 0)
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
		return nil, fmt.Errorf("open /dev/uinput: %w\nSetup required:\n  1. sudo modprobe uinput\n  2. echo 'uinput' | sudo tee /etc/modules-load.d/uinput.conf\n  3. echo 'KERNEL==\"uinput\", GROUP=\"input\", MODE=\"0660\"' | sudo tee /etc/udev/rules.d/99-uinput.rules\n  4. sudo udevadm control --reload-rules && sudo udevadm trigger /dev/uinput\n  5. Ensure your user is in the 'input' group: sudo usermod -aG input $USER", err)
	}

	ok := false
	defer func() {
		if !ok {
			_ = fd.Close()
		}
	}()

	if err := ioctl(fd, uiSetEvBit, uintptr(evKey)); err != nil {
		return nil, fmt.Errorf("UI_SET_EVBIT: %w", err)
	}

	// Register only the key codes this package actually uses.
	// Avoids 767 ioctl syscalls (KEY_MAX) when the keymap tops out at ~127.
	for _, code := range usedKeyCodes() {
		if err := ioctl(fd, uiSetKeyBit, uintptr(code)); err != nil {
			return nil, fmt.Errorf("UI_SET_KEYBIT %d: %w", code, err)
		}
	}

	setup := uinputSetup{ID: inputID{Bustype: busVirtual, Vendor: 0x4D57, Product: 0x4B31, Version: 1}}
	copy(setup.Name[:], name)
	if err := ioctlPtr(fd, uiDevSetup, unsafe.Pointer(&setup)); err != nil {
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

// KeyDown presses a key identified by its evdev keycode.
func (k *VirtualKeyboard) KeyDown(code uint16) error {
	if err := writeEvent(k.fd, evKey, code, 1); err != nil {
		return err
	}
	return syncEvents(k.fd)
}

// KeyUp releases a key identified by its evdev keycode.
func (k *VirtualKeyboard) KeyUp(code uint16) error {
	if err := writeEvent(k.fd, evKey, code, 0); err != nil {
		return err
	}
	return syncEvents(k.fd)
}

// Close destroys the virtual device and closes the file descriptor.
func (k *VirtualKeyboard) Close() error {
	_ = ioctl(k.fd, uiDevDestroy, 0)
	return k.fd.Close()
}
