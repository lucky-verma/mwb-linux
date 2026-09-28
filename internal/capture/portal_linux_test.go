//go:build linux && wayland_portal && cgo

package capture

import (
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

func TestPortalSetupErrorMarksOnlyMissingPortal(t *testing.T) {
	unavailable := portalSetupError("probe", dbus.NewError("org.freedesktop.DBus.Error.UnknownInterface", nil))
	if !errors.Is(unavailable, errPortalCaptureUnavailable) {
		t.Fatalf("missing portal error = %v", unavailable)
	}
	denied := portalSetupError("probe", dbus.NewError("org.freedesktop.portal.Error.NotAllowed", nil))
	if errors.Is(denied, errPortalCaptureUnavailable) {
		t.Fatalf("permission error was marked unavailable: %v", denied)
	}
}

type testPortalZone struct {
	Width  uint32
	Height uint32
	X      int32
	Y      int32
}

func TestDecodePortalZonesAcceptsDBusShapes(t *testing.T) {
	tests := []struct {
		name  string
		value interface{}
	}{
		{
			name: "interface tuples",
			value: [][]interface{}{
				{uint32(1920), uint32(1080), int32(-1920), int32(0)},
				{uint32(2560), uint32(1440), int32(0), int32(-180)},
			},
		},
		{
			name: "struct tuples",
			value: []testPortalZone{
				{Width: 1920, Height: 1080, X: -1920},
				{Width: 2560, Height: 1440, Y: -180},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			zones, err := decodePortalZones(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if len(zones) != 2 || zones[0].X != -1920 || zones[1].Y != -180 {
				t.Fatalf("zones = %#v", zones)
			}
		})
	}
}

func TestDecodePortalZonesRejectsMalformedData(t *testing.T) {
	for _, value := range []interface{}{
		"not zones",
		[][]interface{}{{uint32(1920)}},
		[][]interface{}{{"1920", uint32(1080), int32(0), int32(0)}},
	} {
		if _, err := decodePortalZones(value); err == nil {
			t.Fatalf("decodePortalZones(%#v) succeeded", value)
		}
	}
}

func TestDecodePortalPointAcceptsStructAndArray(t *testing.T) {
	for _, value := range []interface{}{
		portalCursorPosition{X: -10.5, Y: 42.25},
		[]interface{}{-10.5, 42.25},
	} {
		x, y, err := decodePortalPoint(value)
		if err != nil {
			t.Fatal(err)
		}
		if x != -10.5 || y != 42.25 {
			t.Fatalf("point = (%v, %v)", x, y)
		}
	}
}

func TestAxisAccumulatorPreservesFractionalMotion(t *testing.T) {
	var accumulator axisAccumulator
	if x, y := accumulator.take(0.4, -0.4); x != 0 || y != 0 {
		t.Fatalf("first motion = (%d, %d)", x, y)
	}
	if x, y := accumulator.take(0.7, -0.7); x != 1 || y != -1 {
		t.Fatalf("second motion = (%d, %d), want (1, -1)", x, y)
	}
	if x, y := accumulator.take(math.NaN(), math.Inf(1)); x != 0 || y != 0 {
		t.Fatalf("invalid motion = (%d, %d), want zero", x, y)
	}
}

func TestNativeInputDoesNotUseRawDeviceGracePeriod(t *testing.T) {
	c := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	c.active = false
	c.switchSent = time.Now()
	if c.canForwardCapturedInput() {
		t.Fatal("raw input should still honor the post-switch grace period")
	}
	if !c.canForwardNativeInput() {
		t.Fatal("compositor-captured input should be forwarded immediately")
	}
}

func TestContinuousScrollUsesWindowsWheelUnitsAndRemainder(t *testing.T) {
	receiver := &eiReceiver{}
	if x, y := receiver.continuousScroll(5, -5); x != -60 || y != 60 {
		t.Fatalf("scroll = (%d, %d), want (-60, 60)", x, y)
	}
	if x, _ := receiver.continuousScroll(0.04, 0); x != 0 {
		t.Fatalf("fractional scroll = %d, want 0", x)
	}
	if x, _ := receiver.continuousScroll(0.05, 0); x != -1 {
		t.Fatalf("accumulated scroll = %d, want -1", x)
	}
}

func TestPortalDeactivationRestoresLocalOwnership(t *testing.T) {
	owner := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	p := &portalCapture{
		owner:      owner,
		screen:     ScreenInfo{Width: 1920, Height: 1080},
		barrierIDs: map[uint32]struct{}{1: {}},
	}
	p.handleActivated(map[string]dbus.Variant{
		"activation_id":   dbus.MakeVariant(uint32(7)),
		"barrier_id":      dbus.MakeVariant(uint32(1)),
		"cursor_position": dbus.MakeVariant(portalCursorPosition{Y: 540}),
	})
	if owner.IsActive() {
		t.Fatal("portal activation did not transfer ownership to the remote")
	}
	p.handleDeactivated(map[string]dbus.Variant{
		"activation_id": dbus.MakeVariant(uint32(7)),
	})
	if !owner.IsActive() {
		t.Fatal("portal deactivation left ownership stuck on the remote")
	}
}

func TestStalePortalDeactivationDoesNotEndNewActivation(t *testing.T) {
	owner := New(nil, ScreenInfo{Width: 1920, Height: 1080}, "left")
	owner.active = false
	p := &portalCapture{owner: owner, activationID: 8, haveActivation: true}
	p.handleDeactivated(map[string]dbus.Variant{
		"activation_id": dbus.MakeVariant(uint32(7)),
	})
	if owner.IsActive() || !p.haveActivation {
		t.Fatal("stale Deactivated signal ended the current activation")
	}
}

func TestPortalRestoreTokenUsesPrivateStateFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	savePortalRestoreToken("test-token")
	if got := loadPortalRestoreToken(); got != "test-token" {
		t.Fatalf("restore token = %q", got)
	}
	info, err := os.Stat(portalStatePath())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("restore token mode = %o, want 600", mode)
	}
}
