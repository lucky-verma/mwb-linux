//go:build linux

package input

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func canAccessUinput(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("skipping: cannot access /dev/uinput: %v", err)
	}
	_ = f.Close()
}

func TestCreateVirtualMouse(t *testing.T) {
	canAccessUinput(t)

	m, err := CreateVirtualMouse("mwb-test-mouse")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	if err := m.MoveTo(32768, 32768); err != nil {
		t.Errorf("MoveTo: %v", err)
	}
	if err := m.Wheel(1); err != nil {
		t.Errorf("Wheel: %v", err)
	}
}

// TestVirtualMouseHasPointerProperty guards the issue #5 fix: the virtual mouse
// must declare INPUT_PROP_POINTER so libinput classifies it as a pointer device
// on Wayland (without it, compositors apply pointer accel/prediction → woobly).
func TestVirtualMouseHasPointerProperty(t *testing.T) {
	canAccessUinput(t)

	const devName = "mwb-test-prop-mouse"
	m, err := CreateVirtualMouse(devName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	sysPath, err := findInputSysPath(devName)
	if err != nil {
		t.Skipf("could not locate sysfs entry for %q: %v", devName, err)
	}

	props, err := os.ReadFile(filepath.Join(sysPath, "properties"))
	if err != nil {
		t.Fatalf("read properties: %v", err)
	}
	if !propBitSet(string(props), int(inputPropPointer)) {
		t.Errorf("INPUT_PROP_POINTER not set on virtual mouse; properties=%q", strings.TrimSpace(string(props)))
	}
}

// findInputSysPath locates /sys/class/input/inputN whose `name` matches devName.
func findInputSysPath(devName string) (string, error) {
	entries, err := os.ReadDir("/sys/class/input")
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "input") || strings.HasPrefix(e.Name(), "input_") {
			continue
		}
		nameBytes, err := os.ReadFile(filepath.Join("/sys/class/input", e.Name(), "name"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(nameBytes)) == devName {
			return filepath.Join("/sys/class/input", e.Name()), nil
		}
	}
	return "", os.ErrNotExist
}

// propBitSet parses the kernel's hex-string property bitmap (e.g. "1\n" or "0\n")
// and returns whether the given bit index is set.
func propBitSet(props string, bit int) bool {
	props = strings.TrimSpace(props)
	if props == "" {
		return false
	}
	// Bitmap is space-separated longs, MSB-first. For INPUT_PROP_POINTER (bit 0)
	// the rightmost long's bit 0 is what we want.
	fields := strings.Fields(props)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	v, err := strconv.ParseUint(last, 16, 64)
	if err != nil {
		return false
	}
	return v&(1<<uint(bit)) != 0
}

func TestCreateVirtualKeyboard(t *testing.T) {
	canAccessUinput(t)

	k, err := CreateVirtualKeyboard("mwb-test-kbd")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = k.Close() }()

	if err := k.KeyDown(KEY_A); err != nil {
		t.Errorf("KeyDown: %v", err)
	}
	if err := k.KeyUp(KEY_A); err != nil {
		t.Errorf("KeyUp: %v", err)
	}
}
