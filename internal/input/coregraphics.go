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
