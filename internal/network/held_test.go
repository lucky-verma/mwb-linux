// internal/network/held_test.go
package network

import (
	"sort"
	"testing"
)

type recordInput struct{ events []string }

func (r *recordInput) MoveTo(x, y int32) error   { return nil }
func (r *recordInput) Wheel(int32) error         { return nil }
func (r *recordInput) HWheel(int32) error        { return nil }
func (r *recordInput) ButtonDown(b uint16) error { r.events = append(r.events, "bd"); return nil }
func (r *recordInput) ButtonUp(b uint16) error   { r.events = append(r.events, "bu"); return nil }
func (r *recordInput) KeyDown(c uint16) error    { r.events = append(r.events, "kd"); return nil }
func (r *recordInput) KeyUp(c uint16) error      { r.events = append(r.events, "ku"); return nil }

func TestHeldInputReleasesOnlyWhatIsStillDown(t *testing.T) {
	dev := &recordInput{}
	held := NewHeldInput(dev, dev)
	mouse, kb := held.Devices()
	_ = kb.KeyDown(29) // Ctrl, still held
	_ = kb.KeyDown(30)
	_ = kb.KeyUp(30) // released normally
	_ = mouse.ButtonDown(0x110)
	dev.events = nil

	held.Release()
	sort.Strings(dev.events)
	if len(dev.events) != 2 || dev.events[0] != "bu" || dev.events[1] != "ku" {
		t.Fatalf("release sent %v, want one key up and one button up", dev.events)
	}
	dev.events = nil
	held.Release()
	if len(dev.events) != 0 {
		t.Fatalf("a second release sent %v", dev.events)
	}
}
