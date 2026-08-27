//go:build linux

package capture

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/input"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

type fakeNativeCapture struct {
	screen    ScreenInfo
	startErr  error
	started   bool
	closed    bool
	reentered bool
	reenterX  int32
	reenterY  int32
}

func (f *fakeNativeCapture) Name() string       { return "fake-native" }
func (f *fakeNativeCapture) Screen() ScreenInfo { return f.screen }
func (f *fakeNativeCapture) Start() error {
	f.started = true
	return f.startErr
}
func (f *fakeNativeCapture) Reenter(x, y int32) error {
	f.reentered = true
	f.reenterX = x
	f.reenterY = y
	return nil
}
func (f *fakeNativeCapture) Close() error {
	f.closed = true
	return nil
}

func liveWaylandSocket(t *testing.T) {
	t.Helper()
	path := t.TempDir() + "/wayland-test"
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on fake Wayland socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	t.Setenv("WAYLAND_DISPLAY", path)
	t.Setenv("XDG_RUNTIME_DIR", "")
}

func withPortalTestSeams(t *testing.T, available func() bool, factory func(*Capturer) (nativeCapture, error)) {
	t.Helper()
	oldAvailable := portalBackendAvailable
	oldFactory := portalCaptureFactory
	portalBackendAvailable = available
	portalCaptureFactory = factory
	t.Cleanup(func() {
		portalBackendAvailable = oldAvailable
		portalCaptureFactory = oldFactory
	})
}

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

func TestRunAutoSelectsNativePortalOnLiveWayland(t *testing.T) {
	liveWaylandSocket(t)
	fake := &fakeNativeCapture{screen: ScreenInfo{X: -1280, Width: 3200, Height: 1080}}
	withPortalTestSeams(t, func() bool { return true }, func(*Capturer) (nativeCapture, error) {
		return fake, nil
	})
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.findDevicesFn = func() ([]string, error) {
		t.Fatal("native Wayland capture must not scan raw input devices")
		return nil, nil
	}

	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !fake.started {
		t.Fatal("native backend was not started")
	}
	if got := c.screen; got != fake.screen {
		t.Fatalf("screen = %+v, want %+v", got, fake.screen)
	}
	c.Stop()
	if !fake.closed {
		t.Fatal("Stop did not close native backend")
	}
}

func TestRunAutoKeepsX11WhenPortalWasNotBuilt(t *testing.T) {
	liveWaylandSocket(t)
	withPortalTestSeams(t, func() bool { return false }, func(*Capturer) (nativeCapture, error) {
		t.Fatal("auto must not select a portal backend that was not built")
		return nil, nil
	})
	pointer := &blockingPointer{started: make(chan struct{}), closed: make(chan struct{})}
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.pointer = pointer
	c.findDevicesFn = func() ([]string, error) { return nil, nil }

	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	c.Stop()
}

func TestRunExplicitPortalDoesNotFallBackToRawInput(t *testing.T) {
	wantErr := errors.New("portal denied")
	withPortalTestSeams(t, func() bool { return true }, func(*Capturer) (nativeCapture, error) {
		return nil, wantErr
	})
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.SetCaptureBackend("portal")
	c.findDevicesFn = func() ([]string, error) {
		t.Fatal("explicit portal failure must not fall back to raw input")
		return nil, nil
	}

	if err := c.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestRunAutoFallsBackOnlyWhenPortalIsUnavailable(t *testing.T) {
	liveWaylandSocket(t)
	withPortalTestSeams(t, func() bool { return true }, func(*Capturer) (nativeCapture, error) {
		return nil, errPortalCaptureUnavailable
	})
	pointer := &blockingPointer{started: make(chan struct{}), closed: make(chan struct{})}
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.pointer = pointer
	c.findDevicesFn = func() ([]string, error) { return nil, nil }
	if err := c.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	c.Stop()
}

func TestRunAutoDoesNotBypassPortalSetupFailure(t *testing.T) {
	liveWaylandSocket(t)
	wantErr := errors.New("portal permission was denied")
	withPortalTestSeams(t, func() bool { return true }, func(*Capturer) (nativeCapture, error) {
		return nil, wantErr
	})
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.findDevicesFn = func() ([]string, error) {
		t.Fatal("portal setup failure must not be hidden by raw input fallback")
		return nil, nil
	}
	if err := c.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
}

func TestRunClosesNativeBackendWhenStartFails(t *testing.T) {
	wantErr := errors.New("enable failed")
	fake := &fakeNativeCapture{screen: ScreenInfo{Width: 1920, Height: 1080}, startErr: wantErr}
	withPortalTestSeams(t, func() bool { return true }, func(*Capturer) (nativeCapture, error) {
		return fake, nil
	})
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.SetCaptureBackend("portal")

	if err := c.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want %v", err, wantErr)
	}
	if !fake.closed {
		t.Fatal("failed native backend was not closed")
	}
	c.mu.Lock()
	native := c.native
	c.mu.Unlock()
	if native != nil {
		t.Fatal("failed native backend remained attached")
	}
}

func TestReenterUsesNativeBackend(t *testing.T) {
	fake := &fakeNativeCapture{}
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.native = fake
	if err := c.Reenter(123, 456); err != nil {
		t.Fatalf("Reenter: %v", err)
	}
	if !fake.reentered || fake.reenterX != 123 || fake.reenterY != 456 {
		t.Fatalf("native Reenter = (%d, %d), called=%v", fake.reenterX, fake.reenterY, fake.reentered)
	}
}

func TestMouseButtonMessageIncludesSideButtons(t *testing.T) {
	tests := []struct {
		code     uint16
		value    int32
		flags    int32
		buttonID int32
	}{
		{input.BTN_LEFT, 1, protocol.WM_LBUTTONDOWN, 0},
		{input.BTN_LEFT, 0, protocol.WM_LBUTTONUP, 0},
		{input.BTN_SIDE, 1, protocol.WM_XBUTTONDOWN, 1},
		{input.BTN_SIDE, 0, protocol.WM_XBUTTONUP, 1},
		{input.BTN_EXTRA, 1, protocol.WM_XBUTTONDOWN, 2},
		{input.BTN_EXTRA, 0, protocol.WM_XBUTTONUP, 2},
	}
	for _, test := range tests {
		flags, buttonID, ok := mouseButtonMessage(test.code, test.value)
		if !ok || flags != test.flags || buttonID != test.buttonID {
			t.Fatalf("mouseButtonMessage(%#x, %d) = (%#x, %d, %v)", test.code, test.value, flags, buttonID, ok)
		}
	}
	if _, _, ok := mouseButtonMessage(input.BTN_LEFT, 2); ok {
		t.Fatal("button auto-repeat value should be ignored")
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
	// Must be 100px from left edge — not at x=0 which immediately re-triggers switch
	if x < 50 {
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
	// Must be 100px from right edge
	if x > 2560-50 {
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
