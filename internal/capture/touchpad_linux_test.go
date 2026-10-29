//go:build linux

package capture

import (
	"sync"
	"testing"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

type recordedTouchpad struct {
	dx, dy  int32
	scroll  int32
	hscroll int32
	buttons []int32
}

func (r *recordedTouchpad) touchpadMove(dx, dy int32)   { r.dx += dx; r.dy += dy }
func (r *recordedTouchpad) touchpadScroll(ticks int32)  { r.scroll += ticks }
func (r *recordedTouchpad) touchpadHScroll(ticks int32) { r.hscroll += ticks }
func (r *recordedTouchpad) touchpadButton(f int32)      { r.buttons = append(r.buttons, f) }

// pad replays evdev frames into a touchpad with this laptop's 31 units/mm.
type pad struct {
	tp  *touchpad
	out touchpadOutput
	t   int64 // microseconds
}

func newPad(out touchpadOutput) *pad {
	return &pad{tp: &touchpad{resX: 31, resY: 31}, out: out, t: 1_000_000}
}

func (p *pad) ev(typ, code uint16, v int32) {
	p.tp.handle(inputEvent{Sec: p.t / 1_000_000, Usec: p.t % 1_000_000, Type: typ, Code: code, Value: v}, p.out)
}

func (p *pad) syn(advanceMicros int64) {
	p.t += advanceMicros
	p.ev(evSyn, synReport, 0)
}

func (p *pad) down(tool uint16, x, y int32) {
	p.ev(evKey, uint16(btnTouch), 1)
	p.ev(evKey, tool, 1)
	p.ev(uint16(evAbsType), uint16(absX), x)
	p.ev(uint16(evAbsType), uint16(absY), y)
	p.syn(0)
}

func (p *pad) at(x, y int32, advanceMicros int64) {
	p.ev(uint16(evAbsType), uint16(absX), x)
	p.ev(uint16(evAbsType), uint16(absY), y)
	p.syn(advanceMicros)
}

func (p *pad) up(tool uint16, advanceMicros int64) {
	p.ev(evKey, uint16(btnTouch), 0)
	p.ev(evKey, tool, 0)
	p.syn(advanceMicros)
}

func TestTouchpadOneFingerMoves(t *testing.T) {
	r := &recordedTouchpad{}
	p := newPad(r)
	p.down(uint16(btnToolFinger), 1000, 1000)
	for i := int32(1); i <= 10; i++ {
		p.at(1000+i*31, 1000-i*15, 10_000) // 1 mm right per 10 ms
	}
	p.up(uint16(btnToolFinger), 10_000)
	if r.dx <= 0 || r.dy >= 0 {
		t.Fatalf("move = (%d,%d), want right and up", r.dx, r.dy)
	}
	// ~10 mm at 100 mm/s: 12 px/mm × gain 2.6 ≈ 330 px.
	if r.dx < 250 || r.dx > 420 {
		t.Fatalf("dx = %d px for 10 mm, want about 330", r.dx)
	}
	if len(r.buttons) != 0 {
		t.Fatalf("a drag must not click: %v", r.buttons)
	}
}

func TestTouchpadTaps(t *testing.T) {
	for _, tc := range []struct {
		tool     uint16
		down, up int32
	}{
		{uint16(btnToolFinger), protocol.WM_LBUTTONDOWN, protocol.WM_LBUTTONUP},
		{btnToolDouble, protocol.WM_RBUTTONDOWN, protocol.WM_RBUTTONUP},
		{btnToolTriple, protocol.WM_MBUTTONDOWN, protocol.WM_MBUTTONUP},
	} {
		r := &recordedTouchpad{}
		p := newPad(r)
		p.down(tc.tool, 1500, 900)
		p.at(1502, 901, 30_000)
		p.up(tc.tool, 40_000)
		if len(r.buttons) != 2 || r.buttons[0] != tc.down || r.buttons[1] != tc.up {
			t.Errorf("tap with tool %#x: buttons %v, want [%d %d]", tc.tool, r.buttons, tc.down, tc.up)
		}
	}
}

func TestTouchpadLongTouchDoesNotClick(t *testing.T) {
	r := &recordedTouchpad{}
	p := newPad(r)
	p.down(uint16(btnToolFinger), 1500, 900)
	p.at(1500, 900, 400_000)
	p.up(uint16(btnToolFinger), 10_000)
	if len(r.buttons) != 0 {
		t.Fatalf("resting finger clicked: %v", r.buttons)
	}
}

func TestTouchpadTwoFingerScrollFollowsFingers(t *testing.T) {
	r := &recordedTouchpad{}
	p := newPad(r)
	p.down(btnToolDouble, 1500, 600)
	for i := int32(1); i <= 20; i++ {
		p.at(1500, 600+i*31, 10_000) // fingers move down 20 mm
	}
	p.up(btnToolDouble, 10_000)
	if r.scroll != 5 {
		t.Fatalf("scroll = %d ticks for 20 mm down, want 5 (wheel up)", r.scroll)
	}
	if r.dx != 0 || r.dy != 0 {
		t.Fatalf("two-finger scroll moved the pointer (%d,%d)", r.dx, r.dy)
	}
}

func TestTouchpadTwoFingerHorizontalScroll(t *testing.T) {
	r := &recordedTouchpad{}
	p := newPad(r)
	p.down(btnToolDouble, 1500, 600)
	for i := int32(1); i <= 20; i++ {
		p.at(1500+i*31, 600+i*4, 10_000) // 20 mm right, drifting 2.6 mm down
	}
	p.up(btnToolDouble, 10_000)
	if r.hscroll != -5 {
		t.Fatalf("horizontal scroll = %d ticks for 20 mm right, want -5 (scroll left)", r.hscroll)
	}
	if r.scroll != 0 {
		t.Fatalf("a horizontal swipe also scrolled vertically (%d)", r.scroll)
	}
}

func TestTouchpadClickpadPressWithTwoFingersRightClicks(t *testing.T) {
	r := &recordedTouchpad{}
	p := newPad(r)
	p.down(btnToolDouble, 1500, 1800)
	p.ev(evKey, btnLeft, 1)
	p.syn(50_000)
	p.ev(evKey, btnLeft, 0)
	p.syn(50_000)
	p.up(btnToolDouble, 10_000)
	want := []int32{protocol.WM_RBUTTONDOWN, protocol.WM_RBUTTONUP}
	if len(r.buttons) != 2 || r.buttons[0] != want[0] || r.buttons[1] != want[1] {
		t.Fatalf("buttons %v, want %v (and no extra tap click)", r.buttons, want)
	}
}

// Before, the first touchpad frame while remote handed control back to this
// machine. It must now drive the remote cursor, and re-arm the Wayland edge
// while local.
func TestTouchpadDrivesRemoteAndRearmsEdge(t *testing.T) {
	c, _ := newFakeIsolated(t, 1)
	c.screen = ScreenInfo{Width: 1920, Height: 1080}
	c.edgeSide = "left"
	c.remoteW, c.remoteH = 1920, 1080
	c.remoteX, c.remoteY = 1700, 500
	c.canReturn = true
	var mu sync.Mutex
	var sent []protocol.Packet
	c.sendFn = func(pkt *protocol.Packet) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, *pkt)
		return nil
	}

	p := newPad(touchpadSink{c})
	p.down(uint16(btnToolFinger), 1000, 1000)
	for i := int32(1); i <= 5; i++ {
		p.at(1000-i*31, 1000, 10_000)
	}
	p.up(uint16(btnToolFinger), 10_000)
	if c.IsActive() {
		t.Fatal("touchpad motion returned control to this machine")
	}
	c.mu.Lock()
	x := c.remoteX
	c.mu.Unlock()
	if x >= 1700 {
		t.Fatalf("remote cursor did not move left: x=%d", x)
	}
	mu.Lock()
	n := len(sent)
	mu.Unlock()
	if n == 0 {
		t.Fatal("no mouse packets forwarded")
	}

	// Back on this machine with the Wayland edge disarmed: moving inward
	// (right, for a left edge) on the touchpad re-arms it.
	c.SetActive(true)
	c.edge = &fakeEdge{hits: make(chan int32)}
	c.mu.Lock()
	c.canSwitch = false
	c.mu.Unlock()
	p.down(uint16(btnToolFinger), 1000, 1000)
	for i := int32(1); i <= 5; i++ {
		p.at(1000+i*31, 1000, 10_000)
	}
	p.up(uint16(btnToolFinger), 10_000)
	c.mu.Lock()
	armed := c.canSwitch
	c.mu.Unlock()
	if !armed {
		t.Fatal("touchpad motion did not re-arm the edge")
	}
}
