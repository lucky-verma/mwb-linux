//go:build linux

package capture

import "testing"

func TestAccelFactorGrowsWithSpeed(t *testing.T) {
	if f := accelFactor(0.5); f != 1 {
		t.Errorf("slow movement factor %v, want 1", f)
	}
	if lo, hi := accelFactor(3), accelFactor(6); !(1 < lo && lo < hi) {
		t.Errorf("factor does not grow with speed: %v, %v", lo, hi)
	}
	if f := accelFactor(1000); f != accelMaxFactor {
		t.Errorf("fast movement factor %v, want the cap %v", f, accelMaxFactor)
	}
}

func TestPointerAccelUsesFrameSpeed(t *testing.T) {
	var a pointerAccel
	// The first frame has no previous speed.
	if f := a.factorFor(1_000_000, 20, 0); f != 1 {
		t.Fatalf("first frame factor %v, want 1", f)
	}
	a.factorFor(1_000_000, 0, 15) // same frame: Y half
	// 25 counts over 2 ms = 12.5 counts/ms: fast.
	if f := a.factorFor(1_002_000, 1, 0); f <= 1 {
		t.Fatalf("fast frame factor %v, want > 1", f)
	}
	// After a pause, movement starts slow again.
	if f := a.factorFor(1_500_000, 1, 0); f != 1 {
		t.Fatalf("after a pause factor %v, want 1", f)
	}
}
