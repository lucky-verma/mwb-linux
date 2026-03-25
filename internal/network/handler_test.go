// internal/network/handler_test.go
package network

import (
	"testing"

	"github.com/bketelsen/mwb/internal/input"
	"github.com/bketelsen/mwb/internal/protocol"
)

// MockInputDevice records calls for testing.
type MockInputDevice struct {
	MouseMoves  []struct{ X, Y int32 }
	ButtonDowns []uint16
	ButtonUps   []uint16
	Wheels      []int32
	KeyDowns    []uint16
	KeyUps      []uint16
}

func (m *MockInputDevice) MoveTo(x, y int32) error {
	m.MouseMoves = append(m.MouseMoves, struct{ X, Y int32 }{x, y})
	return nil
}
func (m *MockInputDevice) ButtonDown(btn uint16) error {
	m.ButtonDowns = append(m.ButtonDowns, btn)
	return nil
}
func (m *MockInputDevice) ButtonUp(btn uint16) error {
	m.ButtonUps = append(m.ButtonUps, btn)
	return nil
}
func (m *MockInputDevice) Wheel(delta int32) error {
	m.Wheels = append(m.Wheels, delta)
	return nil
}
func (m *MockInputDevice) KeyDown(code uint16) error {
	m.KeyDowns = append(m.KeyDowns, code)
	return nil
}
func (m *MockInputDevice) KeyUp(code uint16) error {
	m.KeyUps = append(m.KeyUps, code)
	return nil
}

func TestHandleMouseMove(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.X = 32768
	pkt.Mouse.Y = 16384
	pkt.Mouse.DwFlags = protocol.WM_MOUSEMOVE

	h.HandlePacket(pkt)

	if len(mock.MouseMoves) != 1 {
		t.Fatalf("expected 1 move, got %d", len(mock.MouseMoves))
	}
	if mock.MouseMoves[0].X != 32768 || mock.MouseMoves[0].Y != 16384 {
		t.Errorf("move = (%d,%d), want (32768,16384)", mock.MouseMoves[0].X, mock.MouseMoves[0].Y)
	}
}

func TestHandleMouseButtons(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_LBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_LEFT {
		t.Errorf("expected BTN_LEFT down, got %v", mock.ButtonDowns)
	}

	pkt.Mouse.DwFlags = protocol.WM_LBUTTONUP
	h.HandlePacket(pkt)
	if len(mock.ButtonUps) != 1 || mock.ButtonUps[0] != input.BTN_LEFT {
		t.Errorf("expected BTN_LEFT up, got %v", mock.ButtonUps)
	}
}

func TestHandleKeyboard(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	expectedCode, ok := input.VKToKeyCode(0x41)
	if !ok {
		t.Fatal("VKToKeyCode(0x41) should map VK_A")
	}

	// Key down: VK_A (0x41)
	pkt := &protocol.Packet{Type: protocol.Keyboard}
	pkt.Keyboard.WVk = 0x41
	pkt.Keyboard.DwFlags = 0

	h.HandlePacket(pkt)
	if len(mock.KeyDowns) != 1 || mock.KeyDowns[0] != expectedCode {
		t.Errorf("expected keycode %d down, got %v", expectedCode, mock.KeyDowns)
	}

	// Key up: VK_A with LLKHF_UP (0x80)
	pkt.Keyboard.DwFlags = protocol.LLKHF_UP
	h.HandlePacket(pkt)
	if len(mock.KeyUps) != 1 || mock.KeyUps[0] != expectedCode {
		t.Errorf("expected keycode %d up, got %v", expectedCode, mock.KeyUps)
	}
}

func TestHandleMouseWheel(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_MOUSEWHEEL
	pkt.Mouse.WheelDelta = 120

	h.HandlePacket(pkt)
	if len(mock.Wheels) != 1 || mock.Wheels[0] != 1 {
		t.Errorf("expected wheel=1, got %v", mock.Wheels)
	}
}

func TestHandleRightButton(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_RBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_RIGHT {
		t.Errorf("expected BTN_RIGHT down, got %v", mock.ButtonDowns)
	}
}

func TestHandleMiddleButton(t *testing.T) {
	mock := &MockInputDevice{}
	h := &Handler{Mouse: mock, Keyboard: mock}

	pkt := &protocol.Packet{Type: protocol.Mouse}
	pkt.Mouse.DwFlags = protocol.WM_MBUTTONDOWN
	h.HandlePacket(pkt)
	if len(mock.ButtonDowns) != 1 || mock.ButtonDowns[0] != input.BTN_MIDDLE {
		t.Errorf("expected BTN_MIDDLE down, got %v", mock.ButtonDowns)
	}
}
