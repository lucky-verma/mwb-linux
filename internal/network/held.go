// internal/network/held.go
package network

import (
	"log/slog"
	"sync"
)

// HeldInput wraps the virtual mouse and keyboard for one connection and
// remembers which keys and buttons that connection is holding down. When the
// connection ends, Release lets them go: otherwise a link that drops while
// Ctrl or a mouse button is down leaves it stuck down on this machine.
type HeldInput struct {
	Mouse    MouseDevice
	Keyboard KeyboardDevice

	mu      sync.Mutex
	keys    map[uint16]bool
	buttons map[uint16]bool
}

// NewHeldInput wraps mouse and keyboard.
func NewHeldInput(mouse MouseDevice, keyboard KeyboardDevice) *HeldInput {
	return &HeldInput{Mouse: mouse, Keyboard: keyboard, keys: map[uint16]bool{}, buttons: map[uint16]bool{}}
}

// Devices returns the tracking mouse and keyboard to hand to a Handler.
func (h *HeldInput) Devices() (MouseDevice, KeyboardDevice) {
	return heldMouse{h}, heldKeyboard{h}
}

// Release lets go of every key and button still held through this input.
func (h *HeldInput) Release() {
	h.mu.Lock()
	keys, buttons := h.keys, h.buttons
	h.keys, h.buttons = map[uint16]bool{}, map[uint16]bool{}
	h.mu.Unlock()
	for code := range keys {
		if err := h.Keyboard.KeyUp(code); err != nil {
			slog.Debug("release held key", "code", code, "err", err)
		}
	}
	for b := range buttons {
		if err := h.Mouse.ButtonUp(b); err != nil {
			slog.Debug("release held button", "button", b, "err", err)
		}
	}
	if n := len(keys) + len(buttons); n > 0 {
		slog.Info("released keys and buttons the other computer was holding down", "count", n)
	}
}

func (h *HeldInput) set(m map[uint16]bool, code uint16, down bool) {
	h.mu.Lock()
	if down {
		m[code] = true
	} else {
		delete(m, code)
	}
	h.mu.Unlock()
}

type heldMouse struct{ h *HeldInput }

func (m heldMouse) MoveTo(x, y int32) error  { return m.h.Mouse.MoveTo(x, y) }
func (m heldMouse) Wheel(delta int32) error  { return m.h.Mouse.Wheel(delta) }
func (m heldMouse) HWheel(delta int32) error { return m.h.Mouse.HWheel(delta) }
func (m heldMouse) ButtonDown(b uint16) error {
	m.h.set(m.h.buttons, b, true)
	return m.h.Mouse.ButtonDown(b)
}
func (m heldMouse) ButtonUp(b uint16) error {
	m.h.set(m.h.buttons, b, false)
	return m.h.Mouse.ButtonUp(b)
}

type heldKeyboard struct{ h *HeldInput }

func (k heldKeyboard) KeyDown(code uint16) error {
	k.h.set(k.h.keys, code, true)
	return k.h.Keyboard.KeyDown(code)
}

func (k heldKeyboard) KeyUp(code uint16) error {
	k.h.set(k.h.keys, code, false)
	return k.h.Keyboard.KeyUp(code)
}
