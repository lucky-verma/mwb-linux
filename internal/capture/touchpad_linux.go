//go:build linux

package capture

import (
	"math"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

// Touchpads report absolute finger positions, not relative motion; the
// compositor (libinput) normally turns them into pointer motion, taps and
// scrolling. While the cursor is remote the touchpad is grabbed, so mwb does
// that translation itself: one finger moves, a quick tap clicks (two fingers
// right-click, three middle-click), two fingers scroll, and pressing the pad
// with two fingers down right-clicks.

const (
	evSyn     = 0x00
	synReport = 0x00

	btnLeft         = 0x110
	btnToolDouble   = 0x14d
	btnToolTriple   = 0x14e
	btnToolQuad     = 0x14f
	btnToolQuint    = 0x148
	inputPropDirect = 0x01 // INPUT_PROP_DIRECT: touchscreens and tablets

	// Pointer speed in pixels per millimetre, scaled by finger speed so slow
	// movement stays precise and a fast swipe crosses the screen.
	touchpadPxPerMM  = 12.0
	touchpadMinGain  = 0.6
	touchpadMaxGain  = 4.0
	touchpadGainMMPS = 50.0 // finger speed (mm/s) that adds 1.0 to the gain

	tapMaxMicros    = 180_000
	tapMaxTravelMM  = 1.5
	scrollMMPerTick = 4.0
)

// touchpad turns one touchpad's frames into pointer actions. Only its own
// monitorDevice goroutine touches it, so it needs no lock.
type touchpad struct {
	resX, resY float64 // units per millimetre
	scale      float64 // accel_multiplier relative to its default; 0 means 1

	x, y          int32
	touching      bool
	tools         uint8 // bit n set: BTN_TOOL for n+1 fingers is down
	wasTouching   bool
	fingers       int
	havePos       bool
	lastX, lastY  int32
	lastT         int64 // microseconds
	downT         int64
	maxFingers    int
	travelMM      float64
	pressed       bool  // clickpad pressed during this touch
	pressedFlagUp int32 // button-up flag for the clickpad press in progress
	fracX, fracY  float64
	scrollMM      float64 // along scrollAxis
	scrollX       bool    // the scroll is horizontal
	scrollLocked  bool    // scrollAxis is chosen for this touch
	scrollDX      float64 // travel before the axis is chosen
	scrollDY      float64
}

// touchpadFor returns a translator when f is a touchpad: an absolute pointer
// with finger tools that is not a direct-touch screen.
func touchpadFor(f *os.File) *touchpad {
	keyBits, err := capabilityBits(f, evKey, keyMax)
	if err != nil || !testBit(keyBits, btnToolFinger) {
		return nil
	}
	absBits, err := capabilityBits(f, evAbsType, absMax)
	if err != nil || !testBit(absBits, absX) || !testBit(absBits, absY) {
		return nil
	}
	var props [8]byte
	const iocRead = 2
	req := uintptr(iocRead<<30 | len(props)<<16 | 'E'<<8 | 0x09) // EVIOCGPROP
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&props[0]))); errno != 0 {
		return nil
	}
	if props[0]&(1<<inputPropDirect) != 0 {
		return nil
	}
	return &touchpad{resX: axisResolution(f, absX), resY: axisResolution(f, absY)}
}

// axisResolution reads units per millimetre for one axis, guessing from a
// 100 mm wide pad when the driver reports none.
func axisResolution(f *os.File, axis uint) float64 {
	var info [6]int32                          // value, minimum, maximum, fuzz, flat, resolution
	req := uintptr(0x80184540) + uintptr(axis) // EVIOCGABS(axis)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(unsafe.Pointer(&info[0]))); errno != 0 {
		return 30
	}
	if info[5] > 0 {
		return float64(info[5])
	}
	if span := info[2] - info[1]; span > 0 {
		return float64(span) / 100
	}
	return 30
}

// touchpadOutput receives what a frame produced.
type touchpadOutput interface {
	touchpadMove(dx, dy int32)
	touchpadScroll(ticks int32)
	touchpadHScroll(ticks int32)
	touchpadButton(flags int32)
}

func (t *touchpad) handle(ev inputEvent, out touchpadOutput) {
	switch ev.Type {
	case uint16(evAbsType):
		switch uint(ev.Code) {
		case absX:
			t.x = ev.Value
		case absY:
			t.y = ev.Value
		}
	case evKey:
		t.key(ev, out)
	case evSyn:
		if ev.Code == synReport {
			t.frame(ev.Sec*1_000_000+ev.Usec, out)
		}
	}
}

func (t *touchpad) key(ev inputEvent, out touchpadOutput) {
	down := ev.Value != 0
	setTool := func(n uint8) {
		if down {
			t.tools |= 1 << (n - 1)
		} else {
			t.tools &^= 1 << (n - 1)
		}
	}
	switch uint(ev.Code) {
	case btnTouch:
		t.touching = down
	case btnToolFinger:
		setTool(1)
	case btnToolDouble:
		setTool(2)
	case btnToolTriple:
		setTool(3)
	case btnToolQuad:
		setTool(4)
	case btnToolQuint:
		setTool(5)
	case btnLeft:
		// A clickpad has one physical button; like libinput's clickfinger
		// mode, two fingers make it a right click.
		if ev.Value == 1 && t.pressedFlagUp == 0 {
			downFlag, upFlag := int32(protocol.WM_LBUTTONDOWN), int32(protocol.WM_LBUTTONUP)
			if t.currentFingers() >= 2 {
				downFlag, upFlag = protocol.WM_RBUTTONDOWN, protocol.WM_RBUTTONUP
			}
			t.pressed = true
			t.pressedFlagUp = upFlag
			out.touchpadButton(downFlag)
		} else if ev.Value == 0 && t.pressedFlagUp != 0 {
			out.touchpadButton(t.pressedFlagUp)
			t.pressedFlagUp = 0
		}
	case uint(input.BTN_RIGHT):
		// Pads with separate physical buttons.
		switch ev.Value {
		case 1:
			out.touchpadButton(protocol.WM_RBUTTONDOWN)
		case 0:
			out.touchpadButton(protocol.WM_RBUTTONUP)
		}
	}
}

func (t *touchpad) currentFingers() int {
	for n := 5; n >= 1; n-- {
		if t.tools&(1<<(n-1)) != 0 {
			return n
		}
	}
	if t.touching {
		return 1
	}
	return 0
}

func (t *touchpad) frame(now int64, out touchpadOutput) {
	fingers := t.currentFingers()
	if t.touching && !t.wasTouching {
		t.downT, t.maxFingers, t.travelMM, t.pressed = now, 0, 0, false
		t.havePos, t.fracX, t.fracY = false, 0, 0
		t.resetScroll()
	}
	if t.touching {
		if fingers != t.fingers {
			t.havePos = false // the reported position jumps to another finger
			t.resetScroll()
		}
		t.maxFingers = max(t.maxFingers, fingers)
		if t.havePos {
			dxMM := float64(t.x-t.lastX) / t.resX
			dyMM := float64(t.y-t.lastY) / t.resY
			t.travelMM += math.Abs(dxMM) + math.Abs(dyMM)
			switch fingers {
			case 1:
				t.move(dxMM, dyMM, now-t.lastT, out)
			case 2:
				t.scroll(dxMM, dyMM, out)
			}
		}
		t.lastX, t.lastY, t.lastT, t.havePos = t.x, t.y, now, true
	} else if t.wasTouching {
		if now-t.downT <= tapMaxMicros && t.travelMM <= tapMaxTravelMM && !t.pressed {
			down, up := int32(protocol.WM_LBUTTONDOWN), int32(protocol.WM_LBUTTONUP)
			switch t.maxFingers {
			case 2:
				down, up = protocol.WM_RBUTTONDOWN, protocol.WM_RBUTTONUP
			case 3:
				down, up = protocol.WM_MBUTTONDOWN, protocol.WM_MBUTTONUP
			}
			out.touchpadButton(down)
			out.touchpadButton(up)
		}
		t.havePos = false
	}
	t.fingers = fingers
	t.wasTouching = t.touching
}

func (t *touchpad) move(dxMM, dyMM float64, dtMicros int64, out touchpadOutput) {
	gain := touchpadMinGain
	if dtMicros > 0 {
		speed := math.Hypot(dxMM, dyMM) / (float64(dtMicros) / 1e6)
		gain = min(touchpadMaxGain, max(touchpadMinGain, touchpadMinGain+speed/touchpadGainMMPS))
	}
	if t.scale > 0 {
		gain *= t.scale
	}
	t.fracX += dxMM * touchpadPxPerMM * gain
	t.fracY += dyMM * touchpadPxPerMM * gain
	dx, dy := int32(t.fracX), int32(t.fracY)
	t.fracX -= float64(dx)
	t.fracY -= float64(dy)
	if dx != 0 || dy != 0 {
		out.touchpadMove(dx, dy)
	}
}

// scrollLockMM is how far two fingers travel before the scroll picks the
// axis it keeps for the rest of the touch, so a slightly diagonal swipe
// scrolls only one way.
const scrollLockMM = 1.5

func (t *touchpad) resetScroll() {
	t.scrollMM, t.scrollX, t.scrollLocked, t.scrollDX, t.scrollDY = 0, false, false, 0, 0
}

// scroll follows the fingers (natural scrolling, the GNOME default): fingers
// moving down bring the content down, which is a wheel-up, and fingers
// moving right bring it right, which is a scroll to the left.
func (t *touchpad) scroll(dxMM, dyMM float64, out touchpadOutput) {
	if !t.scrollLocked {
		t.scrollDX += dxMM
		t.scrollDY += dyMM
		if math.Hypot(t.scrollDX, t.scrollDY) < scrollLockMM {
			return
		}
		t.scrollX = math.Abs(t.scrollDX) > math.Abs(t.scrollDY)
		t.scrollLocked = true
		dxMM, dyMM = t.scrollDX, t.scrollDY // count the travel so far
	}
	if t.scrollX {
		t.scrollMM -= dxMM
	} else {
		t.scrollMM += dyMM
	}
	ticks := int32(t.scrollMM / scrollMMPerTick)
	if ticks == 0 {
		return
	}
	t.scrollMM -= float64(ticks) * scrollMMPerTick
	if t.scrollX {
		out.touchpadHScroll(ticks)
	} else {
		out.touchpadScroll(ticks)
	}
}

// touchpadSink applies a touchpad's actions to the capturer: forwarded while
// the cursor is remote, and used only to re-arm the edge while it is local.
type touchpadSink struct{ c *Capturer }

func (s touchpadSink) remote() bool {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		return false
	}
	// Same grace period as handleEvent: ignore input right after switching.
	return c.switchSent.IsZero() || time.Since(c.switchSent) >= 100*time.Millisecond
}

func (s touchpadSink) touchpadMove(dx, dy int32) {
	c := s.c
	if c.IsActive() {
		c.trackInward(dx, dy)
		return
	}
	if !s.remote() {
		return
	}
	c.mu.Lock()
	c.moveRemoteLocked(dx, dy)
	c.mu.Unlock()
}

func (s touchpadSink) touchpadScroll(ticks int32) {
	if s.remote() {
		s.c.sendMouse(0, 0, ticks*120, protocol.WM_MOUSEWHEEL)
	}
}

func (s touchpadSink) touchpadHScroll(ticks int32) {
	if s.remote() {
		s.c.sendMouse(0, 0, ticks*120, protocol.WM_MOUSEHWHEEL)
	}
}

func (s touchpadSink) touchpadButton(flags int32) {
	if !s.remote() {
		return
	}
	c := s.c
	c.mu.Lock()
	absX := int32(float64(c.remoteX) / float64(c.remoteW) * 65535)
	absY := int32(float64(c.remoteY) / float64(c.remoteH) * 65535)
	c.mu.Unlock()
	c.sendMouse(absX, absY, 0, flags)
}
