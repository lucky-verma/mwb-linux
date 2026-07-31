//go:build linux

package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ioctl encoding ---

// eviocgbit must match the kernel macro
//
//	#define EVIOCGBIT(ev,len) _IOC(_IOC_READ, 'E', 0x20 + (ev), len)
//
// A wrong encoding fails open: the ioctl errors, isGrabTarget returns false for
// everything, and nothing is ever grabbed — local input silently leaks to the
// remote machine instead of being suppressed.
func TestEviocgbit_MatchesKernelMacro(t *testing.T) {
	// Expected values produced by the kernel header itself, not by hand:
	//   #include <linux/input.h>
	//   printf("%#lx", (unsigned long)EVIOCGBIT(EV_KEY, 96));
	tests := []struct {
		name   string
		evType uintptr
		length uintptr
		want   uintptr
	}{
		{"EVIOCGBIT(EV_KEY, 96)", evKey, 96, 0x80604521},
		{"EVIOCGBIT(EV_REL, 2)", evRel, 2, 0x80024522},
		{"EVIOCGBIT(0, 8)", 0, 8, 0x80084520},
	}
	for _, tc := range tests {
		if got := eviocgbit(tc.evType, tc.length); got != tc.want {
			t.Errorf("%s = %#x, want %#x", tc.name, got, tc.want)
		}
	}
}

// EVIOCGRAB is _IOW('E', 0x90, int). Getting this wrong would issue some other
// ioctl entirely against every input device on the machine.
func TestEviocgrab_MatchesKernelMacro(t *testing.T) {
	const want uintptr = 0x40044590 // (_IOC_WRITE<<30)|(4<<16)|('E'<<8)|0x90
	if eviocgrab != want {
		t.Errorf("eviocgrab = %#x, want %#x", eviocgrab, want)
	}
}

// --- bitmask probing ---

func TestTestBit(t *testing.T) {
	// bit 0 and bit 9 set: byte 0 = 0b00000001, byte 1 = 0b00000010
	bits := []byte{0x01, 0x02}
	for _, tc := range []struct {
		bit  uint
		want bool
	}{
		{0, true}, {1, false}, {8, false}, {9, true},
		{15, false},
		{16, false}, // past the end of the slice must not panic
		{9999, false},
	} {
		if got := testBit(bits, tc.bit); got != tc.want {
			t.Errorf("testBit(%d) = %v, want %v", tc.bit, got, tc.want)
		}
	}
}

func TestTestBit_EmptyBitmask(t *testing.T) {
	if testBit(nil, 0) {
		t.Error("testBit on a nil bitmask must be false, not panic")
	}
}

// --- release idempotency ---

// releaseInput runs on every return-to-local, including paths where no grab was
// ever taken. The kernel answers EINVAL there; setGrab must treat that as
// success so a spurious release is not logged as a failure.
func TestSetGrab_ReleaseWithoutGrabIsNotAnError(t *testing.T) {
	f := openAnyInputDevice(t)
	defer f.Close() //nolint:errcheck

	if err := setGrab(f, false); err != nil {
		t.Errorf("release without a prior grab should be a no-op, got %v", err)
	}
}

// --- classification against real hardware ---

// The classifier decides what gets taken away from the display server. Two
// failure modes matter, in opposite directions:
//
//	too narrow  — a keyboard or mouse is missed, and its events leak locally
//	              while the cursor is on the remote machine
//	too broad   — a power or sleep button gets grabbed, which disables the
//	              physical power button, the last-resort recovery path
func TestIsGrabTarget_NeverGrabsButtonsOrSwitches(t *testing.T) {
	devices := readableInputDevices(t)

	// Names that must never classify as a keyboard or pointer. Matching udev's
	// heuristics should already exclude them; this asserts it on real hardware.
	excluded := []string{"power button", "sleep button", "lid switch", "video bus"}

	for _, path := range devices {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		name := strings.ToLower(deviceName(path))
		target := isGrabTarget(f)
		f.Close() //nolint:errcheck

		for _, bad := range excluded {
			if strings.Contains(name, bad) && target {
				t.Errorf("%s (%q) classified as a grab target; grabbing it would "+
					"disable the physical button", path, name)
			}
		}
	}
}

func TestIsGrabTarget_FindsAtLeastOneKeyboardOrPointer(t *testing.T) {
	devices := readableInputDevices(t)

	var found []string
	for _, path := range devices {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if isGrabTarget(f) {
			found = append(found, deviceName(path))
		}
		f.Close() //nolint:errcheck
	}

	if len(found) == 0 {
		t.Skip("no readable keyboard or pointer on this machine — nothing to classify")
	}
	t.Logf("classified %d grab targets: %v", len(found), found)
}

// --- helpers ---

func readableInputDevices(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/dev/input")
	if err != nil {
		t.Skipf("cannot read /dev/input: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		path := filepath.Join("/dev/input", e.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		f.Close() //nolint:errcheck
		out = append(out, path)
	}
	if len(out) == 0 {
		t.Skip("no readable /dev/input/event* devices (needs input group membership)")
	}
	return out
}

func openAnyInputDevice(t *testing.T) *os.File {
	t.Helper()
	for _, path := range readableInputDevices(t) {
		if f, err := os.Open(path); err == nil {
			return f
		}
	}
	t.Skip("no openable input device")
	return nil
}

// deviceName reads the human-readable device name from sysfs, e.g.
// /dev/input/event6 -> /sys/class/input/event6/device/name.
func deviceName(devPath string) string {
	b, err := os.ReadFile(filepath.Join("/sys/class/input", filepath.Base(devPath), "device", "name"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// --- virtual device exclusion ---

// mwb replays remote input through its own uinput devices. Grabbing those would
// swallow the replayed events before they ever reach the display server, so
// remote typing and clicking would stop working with no error anywhere.
func TestIsGrabTarget_NeverGrabsVirtualDevices(t *testing.T) {
	var checked int
	for _, path := range readableInputDevices(t) {
		if !isVirtualDevice(path) {
			continue
		}
		checked++
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		target := isGrabTarget(f)
		name := deviceName(path)
		f.Close() //nolint:errcheck

		if target {
			t.Errorf("virtual device %s (%q) classified as a grab target; "+
				"grabbing injected input creates a feedback loop", path, name)
		}
	}
	if checked == 0 {
		t.Skip("no virtual input devices present to check")
	}
	t.Logf("verified %d virtual devices are excluded", checked)
}

func TestIsVirtualDevice_UnknownPathIsTreatedAsVirtual(t *testing.T) {
	// Failing closed matters: an unreadable node must not be grabbed on a guess.
	if !isVirtualDevice("/dev/input/event-does-not-exist") {
		t.Error("an unresolvable device must be treated as virtual and skipped")
	}
}

// --- hot-plug tracking ---

// Wireless receivers re-enumerate on wake and users plug in devices mid-session.
// A device set captured once at startup goes stale, and an untracked device is
// never grabbed — its motion leaks to the local display while the cursor is on
// the remote machine.
func TestRefreshDevices_AdoptsDevicesPresentOnDisk(t *testing.T) {
	readableInputDevices(t) // skips the test if /dev/input is unreadable

	c := &Capturer{active: true, stopCh: make(chan struct{})}
	c.refreshDevices()
	defer c.Stop()

	c.mu.Lock()
	tracked := len(c.devices)
	c.mu.Unlock()

	if tracked == 0 {
		t.Fatal("refreshDevices tracked no devices despite /dev/input being readable")
	}
}

// A node that disappears must be dropped, not retried on every switch.
func TestRefreshDevices_DropsVanishedDevices(t *testing.T) {
	readableInputDevices(t)

	const ghost = "/dev/input/event-vanished"
	c := &Capturer{
		active:  true,
		stopCh:  make(chan struct{}),
		devices: map[string]*trackedDevice{ghost: {file: os.NewFile(^uintptr(0), ghost), grab: true}},
	}
	c.refreshDevices()
	defer c.Stop()

	c.mu.Lock()
	_, still := c.devices[ghost]
	c.mu.Unlock()

	if still {
		t.Error("a device whose node no longer exists must be dropped from tracking")
	}
}

// Stop() must make later refreshes inert, otherwise addDevice can call wg.Add
// while Stop() is already inside wg.Wait().
func TestRefreshDevices_InertAfterStop(t *testing.T) {
	readableInputDevices(t)

	c := &Capturer{active: true, stopCh: make(chan struct{})}
	c.refreshDevices()
	c.Stop()

	c.refreshDevices()

	c.mu.Lock()
	tracked := len(c.devices)
	c.mu.Unlock()

	if tracked != 0 {
		t.Errorf("refreshDevices after Stop() adopted %d devices; must be inert", tracked)
	}
}

// Stop() must always drain, including when every monitored device is silent.
//
// Regression guard for a real hang: opened blocking, an evdev fd is not
// registered with Go's poller, so Close() does not interrupt a parked read(2).
// monitorDevice then only returns when that device next emits an event, which
// power buttons and audio-jack detect nodes never do, and Stop() waits forever
// in wg.Wait(). addDevice must keep opening devices with O_NONBLOCK.
func TestStop_DrainsEvenWhenDevicesAreSilent(t *testing.T) {
	readableInputDevices(t)

	c := &Capturer{active: true, stopCh: make(chan struct{})}
	c.refreshDevices()

	c.mu.Lock()
	tracked := len(c.devices)
	c.mu.Unlock()
	if tracked == 0 {
		t.Skip("no devices tracked")
	}

	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Stop() did not drain %d monitored devices; "+
			"check that addDevice opens with O_NONBLOCK so Close() interrupts pending reads", tracked)
	}
}
