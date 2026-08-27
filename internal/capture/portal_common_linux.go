//go:build linux

package capture

import (
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errPortalCaptureUnavailable = errors.New("native Wayland InputCapture portal support is unavailable in this build")

var portalCaptureFactory = newPortalCapture
var portalBackendAvailable = portalBackendBuilt

type nativeCapture interface {
	Name() string
	Screen() ScreenInfo
	Start() error
	Reenter(x, y int32) error
	Close() error
}

type portalZone struct {
	Width  uint32
	Height uint32
	X      int32
	Y      int32
}

type portalBarrier struct {
	ID       uint32
	Position portalBarrierPosition
}

type portalBarrierPosition struct {
	X1 int32
	Y1 int32
	X2 int32
	Y2 int32
}

func portalBounds(zones []portalZone) (ScreenInfo, error) {
	if len(zones) == 0 {
		return ScreenInfo{}, errors.New("portal returned no input zones")
	}

	first := zones[0]
	if err := validatePortalZone(first); err != nil {
		return ScreenInfo{}, err
	}
	left := first.X
	top := first.Y
	right := int64(first.X) + int64(first.Width)
	bottom := int64(first.Y) + int64(first.Height)

	for _, zone := range zones[1:] {
		if err := validatePortalZone(zone); err != nil {
			return ScreenInfo{}, err
		}
		zoneRight := int64(zone.X) + int64(zone.Width)
		zoneBottom := int64(zone.Y) + int64(zone.Height)
		if zone.X < left {
			left = zone.X
		}
		if zone.Y < top {
			top = zone.Y
		}
		if zoneRight > right {
			right = zoneRight
		}
		if zoneBottom > bottom {
			bottom = zoneBottom
		}
	}

	width := right - int64(left)
	height := bottom - int64(top)
	if right > math.MaxInt32 || bottom > math.MaxInt32 || width <= 0 || height <= 0 || width > math.MaxInt32 || height > math.MaxInt32 {
		return ScreenInfo{}, errors.New("portal input-zone bounds are invalid")
	}
	return ScreenInfo{X: left, Y: top, Width: int32(width), Height: int32(height)}, nil
}

func validatePortalZone(zone portalZone) error {
	if zone.Width == 0 || zone.Height == 0 {
		return errors.New("portal returned an empty input zone")
	}
	if zone.Width > math.MaxInt32 || zone.Height > math.MaxInt32 {
		return errors.New("portal input zone is too large")
	}
	if int64(zone.X)+int64(zone.Width) > math.MaxInt32 || int64(zone.Y)+int64(zone.Height) > math.MaxInt32 {
		return errors.New("portal input zone is outside the supported coordinate range")
	}
	return nil
}

// barriersForEdge returns one barrier per output that touches the selected
// outer desktop edge. Keeping barriers inside their own zones is required by
// the portal and avoids placing one long barrier across gaps between monitors.
func barriersForEdge(zones []portalZone, edge string) ([]portalBarrier, ScreenInfo, error) {
	bounds, err := portalBounds(zones)
	if err != nil {
		return nil, ScreenInfo{}, err
	}
	if edge != "left" && edge != "right" {
		return nil, ScreenInfo{}, fmt.Errorf("unsupported capture edge %q", edge)
	}

	outerX := bounds.X
	if edge == "right" {
		outerX = bounds.X + bounds.Width
	}

	barriers := make([]portalBarrier, 0, len(zones))
	for _, zone := range zones {
		zoneEdge := zone.X
		if edge == "right" {
			zoneEdge = zone.X + int32(zone.Width)
		}
		if zoneEdge != outerX {
			continue
		}
		barriers = append(barriers, portalBarrier{
			ID: uint32(len(barriers) + 1),
			Position: portalBarrierPosition{
				X1: zoneEdge,
				Y1: zone.Y,
				X2: zoneEdge,
				Y2: zone.Y + int32(zone.Height) - 1,
			},
		})
	}
	if len(barriers) == 0 {
		return nil, ScreenInfo{}, fmt.Errorf("portal has no zones on the %s desktop edge", edge)
	}
	return barriers, bounds, nil
}

func waylandSessionActive() bool {
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if display := os.Getenv("WAYLAND_DISPLAY"); display != "" {
		return waylandSocketAlive(runtimeDir, display)
	}
	if runtimeDir == "" {
		return false
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "wayland-") && !strings.HasSuffix(entry.Name(), ".lock") && waylandSocketAlive(runtimeDir, entry.Name()) {
			return true
		}
	}
	return false
}

func waylandSocketAlive(runtimeDir, display string) bool {
	if display == "" {
		return false
	}
	path := display
	if !filepath.IsAbs(path) {
		if runtimeDir == "" {
			return false
		}
		path = filepath.Join(runtimeDir, display)
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
