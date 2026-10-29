//go:build linux

package capture

import "math"

// While the cursor is remote the mouse is grabbed, so the compositor's pointer
// acceleration no longer applies and mwb has to provide its own. A constant
// gain is either too slow for crossing the screen or too twitchy for aiming;
// like libinput's adaptive profile, the gain grows with how fast the mouse
// moves: accel_multiplier when slow, up to accelMaxFactor times that when fast.
const (
	// Speeds are in device counts per millisecond, which does not depend on
	// the mouse's polling rate. A 1000 DPI mouse moving 25 mm/s is ~1.
	accelThreshold = 1.0  // below this the gain is just accel_multiplier
	accelSlope     = 0.35 // added factor per count/ms above the threshold
	accelMaxFactor = 3.0
	// A pause longer than this starts a new movement at the base gain.
	accelIdleMicros = 100_000
)

// pointerAccel turns the speed of the last motion frame into a gain factor.
// It is used under Capturer.mu.
type pointerAccel struct {
	frameT int64 // timestamp of the frame being accumulated, microseconds
	fx, fy int32 // counts accumulated in that frame
	factor float64
}

// factorFor returns the gain factor for a motion event at time t (µs). Events
// of one frame share a timestamp; when a new frame starts, the finished one
// sets the factor for it.
func (a *pointerAccel) factorFor(t int64, dx, dy int32) float64 {
	if t != a.frameT {
		dt := t - a.frameT
		switch {
		case a.frameT == 0 || dt <= 0 || dt > accelIdleMicros:
			a.factor = 1
		default:
			speed := math.Hypot(float64(a.fx), float64(a.fy)) / (float64(dt) / 1000)
			a.factor = accelFactor(speed)
		}
		a.frameT, a.fx, a.fy = t, 0, 0
	}
	a.fx += dx
	a.fy += dy
	return a.factor
}

// accelFactor maps a speed in counts per millisecond to a gain factor.
func accelFactor(speed float64) float64 {
	return min(accelMaxFactor, max(1, 1+(speed-accelThreshold)*accelSlope))
}
