//go:build linux

package input

import (
	"os"
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
