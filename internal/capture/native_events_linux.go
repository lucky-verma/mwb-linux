//go:build linux && wayland_portal && cgo

package capture

import "github.com/lucky-verma/mwb-linux/internal/protocol"

func (c *Capturer) setNativeScreen(screen ScreenInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if screen.Width > 0 && screen.Height > 0 {
		c.screen = screen
	}
}

func (c *Capturer) canForwardNativeInput() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.active
}

func (c *Capturer) handleNativeMotion(dx, dy int32) {
	if !c.canForwardNativeInput() {
		return
	}
	if dx != 0 {
		c.handleRelWithMultiplier(inputEvent{Type: evRel, Code: relX, Value: dx}, 1.0)
	}
	if dy != 0 {
		c.handleRelWithMultiplier(inputEvent{Type: evRel, Code: relY, Value: dy}, 1.0)
	}
}

func (c *Capturer) handleNativeButton(code uint32, pressed bool) {
	c.handleNativeKey(code, pressed)
}

func (c *Capturer) handleNativeKey(code uint32, pressed bool) {
	if code > uint32(^uint16(0)) || !c.canForwardNativeInput() {
		return
	}
	value := int32(0)
	if pressed {
		value = 1
	}
	c.handleKey(inputEvent{Type: evKey, Code: uint16(code), Value: value})
}

func (c *Capturer) handleNativeScroll(horizontal, vertical int32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active || c.remoteW <= 0 || c.remoteH <= 0 {
		return
	}
	absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
	absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
	if vertical != 0 {
		c.sendMouseLocked(absX, absY, vertical, protocol.WM_MOUSEWHEEL)
	}
	if horizontal != 0 {
		c.sendMouseLocked(absX, absY, horizontal, protocol.WM_MOUSEHWHEEL)
	}
}
