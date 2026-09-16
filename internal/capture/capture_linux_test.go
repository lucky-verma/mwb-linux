//go:build linux

package capture

import (
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type blockingPointer struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (p *blockingPointer) Position() (int32, int32, error) {
	p.startOnce.Do(func() { close(p.started) })
	<-p.closed
	return -1, -1, errors.New("pointer closed")
}

func (p *blockingPointer) MoveTo(x, y int32) error { return nil }

func (p *blockingPointer) Close() {
	p.closeOnce.Do(func() { close(p.closed) })
}

func TestRunUsesPersistentPointerAndStopClosesIt(t *testing.T) {
	pointer := &blockingPointer{started: make(chan struct{}), closed: make(chan struct{})}
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.pointer = pointer
	c.findDevicesFn = func() ([]string, error) { return nil, nil }

	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-pointer.started:
	case <-time.After(time.Second):
		t.Fatal("cursor poll did not query the persistent pointer")
	}

	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not close the pointer and unblock cursor polling")
	}
}

// --- applyAcceleration ---

func TestApplyAcceleration_ZeroDelta(t *testing.T) {
	if got := applyAcceleration(0, defaultAccelMultiplier); got != 0 {
		t.Errorf("applyAcceleration(0) = %d, want 0", got)
	}
}

func TestApplyAcceleration_SmallPositive(t *testing.T) {
	// Values < 1 after scaling should be clamped to 1
	if got := applyAcceleration(1, defaultAccelMultiplier); got < 1 {
		t.Errorf("applyAcceleration(1) = %d, should be >= 1", got)
	}
}

func TestApplyAcceleration_SmallNegative(t *testing.T) {
	if got := applyAcceleration(-1, defaultAccelMultiplier); got > -1 {
		t.Errorf("applyAcceleration(-1) = %d, should be <= -1", got)
	}
}

func TestApplyAcceleration_Symmetry(t *testing.T) {
	for _, delta := range []int32{1, 5, 10, 100} {
		pos := applyAcceleration(delta, defaultAccelMultiplier)
		neg := applyAcceleration(-delta, defaultAccelMultiplier)
		if pos != -neg {
			t.Errorf("acceleration not symmetric: applyAcceleration(%d)=%d, applyAcceleration(%d)=%d",
				delta, pos, -delta, neg)
		}
	}
}

func TestApplyAcceleration_Multiplier(t *testing.T) {
	// Multiplier scales linearly above the sub-pixel clamp.
	if got := applyAcceleration(10, 1.0); got != 10 {
		t.Errorf("applyAcceleration(10, 1.0) = %d, want 10", got)
	}
	if got := applyAcceleration(10, 3.0); got != 30 {
		t.Errorf("applyAcceleration(10, 3.0) = %d, want 30", got)
	}
	// A fractional multiplier still moves at least 1px for a unit delta.
	if got := applyAcceleration(1, 0.5); got != 1 {
		t.Errorf("applyAcceleration(1, 0.5) = %d, want 1 (sub-pixel clamp)", got)
	}
}

// --- SafeEntryPosition ---

func TestSafeEntryPosition_LeftEdge(t *testing.T) {
	c := &Capturer{screen: ScreenInfo{Width: 2560, Height: 1440}, edgeSide: "left"}
	x, y := c.SafeEntryPosition()
	// Land just inside the edge, without a large visible jump.
	if x != entryClearance {
		t.Errorf("left edge: x=%d too close to edge, cursor will re-trigger switch", x)
	}
	// Y should be somewhere reasonable (not 0, not at edge)
	if y <= 0 || y >= 1440 {
		t.Errorf("left edge: y=%d out of screen bounds", y)
	}
}

func TestSafeEntryPosition_RightEdge(t *testing.T) {
	c := &Capturer{screen: ScreenInfo{Width: 2560, Height: 1440}, edgeSide: "right"}
	x, y := c.SafeEntryPosition()
	// Use the same clearance on the right.
	if x != 2560-entryClearance {
		t.Errorf("right edge: x=%d too close to right edge, cursor will re-trigger switch", x)
	}
	if y <= 0 || y >= 1440 {
		t.Errorf("right edge: y=%d out of screen bounds", y)
	}
}

func TestAcceptsReclaim_LeftEdgeOnlyAcceptsLocalLeftLanding(t *testing.T) {
	c := &Capturer{edgeSide: "left"}
	if !c.AcceptsReclaim(1200) {
		t.Fatal("left-edge setup should accept a NextMachine landing near local left edge")
	}
	if c.AcceptsReclaim(65000) {
		t.Fatal("left-edge setup must reject far-right landing from the remote's far-left edge")
	}
}

func TestAcceptsReclaim_RightEdgeOnlyAcceptsLocalRightLanding(t *testing.T) {
	c := &Capturer{edgeSide: "right"}
	if !c.AcceptsReclaim(65000) {
		t.Fatal("right-edge setup should accept a NextMachine landing near local right edge")
	}
	if c.AcceptsReclaim(1200) {
		t.Fatal("right-edge setup must reject far-left landing from the remote's far-right edge")
	}
}

func TestAcceptsActivation_LeftEdgeOnlyAcceptsRemoteRightEdge(t *testing.T) {
	c := &Capturer{active: false, edgeSide: "left", remoteW: 1920, remoteX: 1919}
	if !c.AcceptsActivation() {
		t.Fatal("left-edge setup should accept MachineSwitched from remote right/shared edge")
	}
	c.remoteX = 1720
	if c.AcceptsActivation() {
		t.Fatal("left-edge setup must reject MachineSwitched at the 200px remote entry offset")
	}
	c.remoteX = 0
	if c.AcceptsActivation() {
		t.Fatal("left-edge setup must reject MachineSwitched from remote far-left edge")
	}
}

func TestAcceptsActivation_RightEdgeOnlyAcceptsRemoteLeftEdge(t *testing.T) {
	c := &Capturer{active: false, edgeSide: "right", remoteW: 1920, remoteX: 0}
	if !c.AcceptsActivation() {
		t.Fatal("right-edge setup should accept MachineSwitched from remote left/shared edge")
	}
	c.remoteX = 200
	if c.AcceptsActivation() {
		t.Fatal("right-edge setup must reject MachineSwitched at the 200px remote entry offset")
	}
	c.remoteX = 1919
	if c.AcceptsActivation() {
		t.Fatal("right-edge setup must reject MachineSwitched from remote far-right edge")
	}
}

// --- SetActive mutex invariant ---

// SetActive must NOT hold c.mu when calling applyIsolation.
// applyIsolation acquires c.mu internally, so holding it in SetActive causes deadlock.
// This test catches that regression by running SetActive with a timeout.
func TestSetActive_NoDeadlockOnActivate(t *testing.T) {
	c := &Capturer{
		active:   false,
		stopCh:   make(chan struct{}),
		remoteW:  1920,
		remoteH:  1080,
		edgeSide: "left",
	}
	c.screen = ScreenInfo{Width: 1920, Height: 1080}

	done := make(chan struct{})
	go func() {
		c.SetActive(true)
		close(done)
	}()

	select {
	case <-done:
		// pass — no deadlock
	case <-time.After(3 * time.Second):
		t.Fatal("SetActive deadlocked — check that applyIsolation() is called AFTER c.mu.Unlock()")
	}
}

func TestSetActive_ResetsGatesOnActivate(t *testing.T) {
	c := &Capturer{
		active:    false,
		canSwitch: true,
		canReturn: true,
		stopCh:    make(chan struct{}),
		remoteW:   1920,
		remoteH:   1080,
	}
	c.SetActive(true)

	c.mu.Lock()
	cs := c.canSwitch
	cr := c.canReturn
	c.mu.Unlock()

	// Both gates must be reset on activation — prevents immediate re-trigger
	// of the edge switch before the cursor moves away from the edge.
	if cs {
		t.Error("canSwitch must be false after SetActive(true) — cursor is at edge, must move away first")
	}
	if cr {
		t.Error("canReturn must be false after SetActive(true) — prevents ghost bounce on reconnect")
	}
}

func TestSetActive_NoOpWhenAlreadyActive(t *testing.T) {
	c := &Capturer{
		active:  true,
		stopCh:  make(chan struct{}),
		remoteW: 1920,
		remoteH: 1080,
	}
	// Should not deadlock, should not panic
	done := make(chan struct{})
	go func() {
		c.SetActive(true) // already active — should be a no-op
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SetActive(true) on already-active Capturer deadlocked")
	}
}

func TestSetActive_NotifiesOnlyOnTransitionToLocal(t *testing.T) {
	activations := 0
	c := &Capturer{
		active:      false,
		stopCh:      make(chan struct{}),
		remoteW:     1920,
		remoteH:     1080,
		OnActivated: func() { activations++ },
	}

	c.SetActive(true)
	c.SetActive(true)
	if activations != 1 {
		t.Fatalf("activation callbacks = %d, want exactly one for false -> true", activations)
	}
}

// --- canSwitch / canReturn gates ---

func TestCanSwitchGate_RequiresMoveAwayFromEdge(t *testing.T) {
	c := &Capturer{
		active:    true,
		canSwitch: false, // just activated — must move away from edge first
		edgeSide:  "left",
		screen:    ScreenInfo{Width: 2560, Height: 1440},
	}

	const edgeZone = int32(20)

	// Simulate cursor at x=0 (edge) — canSwitch should NOT arm
	c.mu.Lock()
	if 0 > edgeZone {
		c.canSwitch = true
	}
	armed := c.canSwitch
	c.mu.Unlock()

	if armed {
		t.Error("canSwitch should not arm when cursor is at x=0 (the edge)")
	}

	// Simulate cursor moving to x=100 — canSwitch should arm
	c.mu.Lock()
	if 100 > edgeZone {
		c.canSwitch = true
	}
	armed = c.canSwitch
	c.mu.Unlock()

	if !armed {
		t.Error("canSwitch should arm when cursor moves 100px away from edge")
	}
}

func TestAbsolutePointerRestoresLocalInput(t *testing.T) {
	c, grabbed := newFakeIsolated(t, 1)
	c.applyIsolation()
	if grabbed() != 1 {
		t.Fatal("device not isolated")
	}
	c.handleEvent(inputEvent{Type: uint16(evAbsType), Code: uint16(absX), Value: 123})
	if !c.IsActive() || grabbed() != 0 || c.canSwitch {
		t.Fatal("absolute pointer did not restore input with bounce gate closed")
	}
}

func TestRemoteLandingMatchesTrackedPositionAndRejectsBounce(t *testing.T) {
	for _, width := range []int32{1920, 2560, 3840} {
		for _, edge := range []string{"left", "right"} {
			c := New(nil, ScreenInfo{Width: width, Height: 1080}, edge)
			c.remoteW = width
			pixel, wire := c.remoteEntryLocked()
			want := int32((int64(width)*5243 + 32767) / 65535)
			if edge == "left" {
				want = width - want
			}
			if pixel != want {
				t.Fatalf("landing=%d want %d", pixel, want)
			}
			if wire != int32(int64(pixel)*65535/int64(width)) {
				t.Fatal("wire and tracked positions disagree")
			}
			c.active, c.remoteX = false, pixel
			if c.AcceptsActivation() {
				t.Fatal("accepted a bounce at entry")
			}
			if edge == "left" {
				c.remoteX = width - 1
			} else {
				c.remoteX = 0
			}
			if !c.AcceptsActivation() {
				t.Fatal("rejected shared-edge return")
			}
		}
	}
}

type recordingPointer struct {
	x, y  int32
	moved bool
}

func (p *recordingPointer) Position() (int32, int32, error) { return p.x, p.y, nil }
func (p *recordingPointer) MoveTo(x, y int32) error         { p.x, p.y, p.moved = x, y, true; return nil }
func (p *recordingPointer) Close()                          {}

func TestReturnKeepsHeightAndRecentersBeforeRelease(t *testing.T) {
	c, grabbed := newFakeIsolated(t, 1)
	c.screen = ScreenInfo{Width: 2560, Height: 1440}
	c.edgeSide, c.remoteH, c.remoteY = "left", 1080, 270
	c.applyIsolation()
	pointer := &recordingPointer{}
	c.pointer = pointer
	c.setGrabFn = func(_ *os.File, grab bool) error {
		if !grab && !pointer.moved {
			t.Fatal("released input before moving inside")
		}
		return nil
	}
	c.SetActive(true)
	if pointer.x != entryClearance || pointer.y != 360 || grabbed() != 0 || c.canSwitch {
		t.Fatalf("incorrect return: position=%d,%d grabbed=%d canSwitch=%v", pointer.x, pointer.y, grabbed(), c.canSwitch)
	}
}
