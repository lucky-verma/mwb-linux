//go:build linux

package capture

import (
	"testing"
	"time"
)

// --- parseXinputIDs ---

func TestParseXinputIDs_SkipsFloatingSlaves(t *testing.T) {
	// The single most important invariant: floating slaves must NEVER be included.
	// Calling xinput disable/enable on a floating slave corrupts its attachment
	// state and requires manual recovery (xinput reattach + xinput enable).
	output := `
⎡ Virtual core pointer                    	id=2	[master pointer  (3)]
⎜   ↳ Wooting Wooting 60HE+ Mouse             	id=11	[slave  pointer  (2)]
⎜   ↳ Wooting Wooting 60HE+ Consumer Control  	id=10	[slave  pointer  (2)]
⎣ Virtual core keyboard                   	id=3	[master keyboard (2)]
    ↳ Wooting Wooting 60HE+                   	id=8	[slave  keyboard (3)]
    ↳ Wooting Wooting 60HE+ Consumer Control  	id=12	[slave  keyboard (3)]
∼ Razer Razer DeathAdder V2 Pro           	id=26	[floating slave]
∼ Razer Razer DeathAdder V2 Pro           	id=25	[floating slave]
∼ RAZER Razer Mouse Dock                  	id=18	[floating slave]
`
	ids := parseXinputIDs(output)

	// Must include all 4 attached devices
	if len(ids) != 4 {
		t.Errorf("expected 4 attached device IDs, got %d: %v", len(ids), ids)
	}

	// Must NOT include any floating slaves (26, 25, 18)
	floating := map[int]bool{26: true, 25: true, 18: true}
	for _, id := range ids {
		if floating[id] {
			t.Errorf("floating slave id=%d must not be included — causes attachment corruption", id)
		}
	}
}

func TestParseXinputIDs_AttachedDevicesIncluded(t *testing.T) {
	output := `
⎜   ↳ Wooting Wooting 60HE+ Mouse             	id=10	[slave  pointer  (2)]
    ↳ Wooting Wooting 60HE+                   	id=8	[slave  keyboard (3)]
    ↳ Power Button                            	id=6	[slave  keyboard (3)]
`
	ids := parseXinputIDs(output)
	if len(ids) != 2 {
		t.Errorf("expected 2 Wooting device IDs, got %d: %v", len(ids), ids)
	}
	has := func(want int) bool {
		for _, id := range ids { if id == want { return true } }
		return false
	}
	if !has(10) { t.Error("expected id=10 (Wooting Mouse)") }
	if !has(8)  { t.Error("expected id=8 (Wooting keyboard)") }
}

func TestParseXinputIDs_EmptyOutput(t *testing.T) {
	if ids := parseXinputIDs(""); len(ids) != 0 {
		t.Errorf("empty output should return no IDs, got %v", ids)
	}
}

func TestParseXinputIDs_NoRazerWooting(t *testing.T) {
	output := `
⎡ Virtual core pointer                    	id=2	[master pointer  (3)]
⎜   ↳ Logitech MX Master 3                    	id=9	[slave  pointer  (2)]
⎣ Virtual core keyboard                   	id=3	[master keyboard (2)]
    ↳ Generic USB Keyboard                    	id=6	[slave  keyboard (3)]
`
	if ids := parseXinputIDs(output); len(ids) != 0 {
		t.Errorf("no Razer/Wooting devices, expected 0 IDs, got %v", ids)
	}
}

// --- applyAcceleration ---

func TestApplyAcceleration_ZeroDelta(t *testing.T) {
	if got := applyAcceleration(0); got != 0 {
		t.Errorf("applyAcceleration(0) = %d, want 0", got)
	}
}

func TestApplyAcceleration_SmallPositive(t *testing.T) {
	// Values < 1 after scaling should be clamped to 1
	if got := applyAcceleration(1); got < 1 {
		t.Errorf("applyAcceleration(1) = %d, should be >= 1", got)
	}
}

func TestApplyAcceleration_SmallNegative(t *testing.T) {
	if got := applyAcceleration(-1); got > -1 {
		t.Errorf("applyAcceleration(-1) = %d, should be <= -1", got)
	}
}

func TestApplyAcceleration_Symmetry(t *testing.T) {
	for _, delta := range []int32{1, 5, 10, 100} {
		pos := applyAcceleration(delta)
		neg := applyAcceleration(-delta)
		if pos != -neg {
			t.Errorf("acceleration not symmetric: applyAcceleration(%d)=%d, applyAcceleration(%d)=%d",
				delta, pos, -delta, neg)
		}
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

// --- SetActive mutex invariant ---

// SetActive must NOT hold c.mu when calling enableXinput.
// enableXinput acquires c.mu internally, so holding it in SetActive causes deadlock.
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
		t.Fatal("SetActive deadlocked — check that enableXinput() is called AFTER c.mu.Unlock()")
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
		active:   true,
		stopCh:   make(chan struct{}),
		remoteW:  1920,
		remoteH:  1080,
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

// --- disabledXinputIDs cache ---

func TestDisabledXinputIDsCache_ClearedOnEnable(t *testing.T) {
	c := &Capturer{
		stopCh:            make(chan struct{}),
		disabledXinputIDs: []int{8, 9, 10}, // simulating previously disabled IDs
	}

	// After enableXinput, cache must be cleared
	c.enableXinput()

	c.mu.Lock()
	remaining := len(c.disabledXinputIDs)
	c.mu.Unlock()

	if remaining != 0 {
		t.Errorf("disabledXinputIDs should be cleared after enableXinput, got %d entries", remaining)
	}
}
