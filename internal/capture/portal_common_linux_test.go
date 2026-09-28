//go:build linux

package capture

import (
	"math"
	"net"
	"path/filepath"
	"testing"
)

func TestBarriersForEdgeSingleMonitor(t *testing.T) {
	zones := []portalZone{{Width: 1920, Height: 1080}}
	left, bounds, err := barriersForEdge(zones, "left")
	if err != nil {
		t.Fatal(err)
	}
	if bounds != (ScreenInfo{Width: 1920, Height: 1080}) {
		t.Fatalf("bounds = %#v", bounds)
	}
	if got := left[0].Position; got != (portalBarrierPosition{X1: 0, Y1: 0, X2: 0, Y2: 1079}) {
		t.Fatalf("left barrier = %#v", got)
	}

	right, _, err := barriersForEdge(zones, "right")
	if err != nil {
		t.Fatal(err)
	}
	if got := right[0].Position; got != (portalBarrierPosition{X1: 1920, Y1: 0, X2: 1920, Y2: 1079}) {
		t.Fatalf("right barrier = %#v", got)
	}
}

func TestPortalBoundsRejectsCoordinateOverflow(t *testing.T) {
	tests := []portalZone{
		{Width: 1, Height: 1, X: math.MaxInt32},
		{Width: 1, Height: 1, Y: math.MaxInt32},
		{Width: math.MaxInt32 + 1, Height: 1},
	}
	for _, zone := range tests {
		if _, err := portalBounds([]portalZone{zone}); err == nil {
			t.Fatalf("portalBounds(%+v) succeeded, want an overflow error", zone)
		}
	}
}

func TestBarriersForEdgeUsesOnlyOuterZones(t *testing.T) {
	zones := []portalZone{
		{Width: 1920, Height: 1080, X: -1920, Y: 180},
		{Width: 2560, Height: 1440, X: 0, Y: 0},
		{Width: 2560, Height: 1440, X: 2560, Y: 0},
	}
	left, bounds, err := barriersForEdge(zones, "left")
	if err != nil {
		t.Fatal(err)
	}
	if bounds != (ScreenInfo{X: -1920, Width: 7040, Height: 1440}) {
		t.Fatalf("bounds = %#v", bounds)
	}
	if len(left) != 1 || left[0].Position.X1 != -1920 {
		t.Fatalf("left barriers = %#v", left)
	}
	right, _, err := barriersForEdge(zones, "right")
	if err != nil {
		t.Fatal(err)
	}
	if len(right) != 1 || right[0].Position.X1 != 5120 {
		t.Fatalf("right barriers = %#v", right)
	}
}

func TestBarriersForEdgeKeepsSeparateVerticalSegments(t *testing.T) {
	zones := []portalZone{
		{Width: 1920, Height: 1080, X: 0, Y: 0},
		{Width: 1920, Height: 1080, X: 0, Y: 1080},
	}
	barriers, _, err := barriersForEdge(zones, "left")
	if err != nil {
		t.Fatal(err)
	}
	if len(barriers) != 2 {
		t.Fatalf("barrier count = %d, want 2", len(barriers))
	}
	if barriers[0].Position.Y2 != 1079 || barriers[1].Position.Y1 != 1080 {
		t.Fatalf("barriers = %#v", barriers)
	}
}

func TestWaylandSessionActiveRequiresLiveSocket(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-7")
	if waylandSessionActive() {
		t.Fatal("missing Wayland socket reported active")
	}

	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "wayland-7"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if !waylandSessionActive() {
		t.Fatal("live Wayland socket was not detected")
	}
}
