//go:build linux

package capture

import (
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

type fakeEdge struct {
	hits      chan int32
	closeOnce sync.Once
}

func (e *fakeEdge) Hits() <-chan int32 { return e.hits }
func (e *fakeEdge) Raise()             {}
func (e *fakeEdge) Close()             { e.closeOnce.Do(func() { close(e.hits) }) }

func TestIsWaylandSession(t *testing.T) {
	for _, tc := range []struct {
		display, session string
		want             bool
	}{
		{"wayland-0", "wayland", true},
		{"", "wayland", true},
		{"wayland-0", "", true},
		{"", "x11", false},
	} {
		t.Setenv("WAYLAND_DISPLAY", tc.display)
		t.Setenv("XDG_SESSION_TYPE", tc.session)
		if got := isWaylandSession(); got != tc.want {
			t.Errorf("WAYLAND_DISPLAY=%q XDG_SESSION_TYPE=%q: got %v, want %v", tc.display, tc.session, got, tc.want)
		}
	}
}

// On Wayland the polled X11 position is frozen, so a touch on the edge window
// must switch to the remote on its own, and re-arming must come from evdev.
func TestEdgeWindowHitSwitchesAndInwardMotionRearms(t *testing.T) {
	c, grabbed := newFakeIsolated(t, 2)
	c.screen = ScreenInfo{Width: 1920, Height: 1080}
	c.edgeSide = "right"
	c.active = true
	c.canSwitch = true
	c.remoteW, c.remoteH = 1920, 1080
	c.pointer = &recordingPointer{x: 939, y: 79} // frozen XWayland position
	edge := &fakeEdge{hits: make(chan int32, 1)}
	c.edge = edge
	var mu sync.Mutex
	var sent []protocol.PackageType
	c.sendFn = func(p *protocol.Packet) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, p.Type)
		return nil
	}

	c.wg.Add(1)
	go func() { defer c.wg.Done(); c.pollCursorEdge() }()
	t.Cleanup(func() { close(c.stopCh); edge.Close(); c.wg.Wait() })

	edge.hits <- 540
	deadline := time.Now().Add(time.Second)
	for c.IsActive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.IsActive() || grabbed() != 2 {
		t.Fatalf("edge hit did not switch: active=%v grabbed=%d", c.IsActive(), grabbed())
	}

	// Coming back disarms the edge; touching it again must not switch.
	c.SetActive(true)
	time.Sleep(150 * time.Millisecond) // clear the switch debounce
	edge.hits <- 540
	time.Sleep(50 * time.Millisecond)
	if !c.IsActive() {
		t.Fatal("edge hit switched before the pointer moved inward")
	}

	// Outward pushes against the edge do not count toward re-arming.
	c.handleEvent(inputEvent{Type: evRel, Code: relX, Value: 50})
	c.handleEvent(inputEvent{Type: evRel, Code: relX, Value: -(rearmDistance - 1)})
	c.mu.Lock()
	armed := c.canSwitch
	c.mu.Unlock()
	if armed {
		t.Fatal("re-armed before moving rearmDistance inward")
	}
	c.handleEvent(inputEvent{Type: evRel, Code: relX, Value: -1})
	c.mu.Lock()
	armed = c.canSwitch
	c.mu.Unlock()
	if !armed {
		t.Fatal("did not re-arm after moving rearmDistance inward")
	}

	edge.hits <- 100
	deadline = time.Now().Add(time.Second)
	for c.IsActive() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.IsActive() {
		t.Fatal("re-armed edge hit did not switch")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	mouse := 0
	for _, typ := range sent {
		if typ == protocol.Mouse {
			mouse++
		}
	}
	if mouse == 0 {
		t.Fatal("no entry burst sent to the remote")
	}
}
