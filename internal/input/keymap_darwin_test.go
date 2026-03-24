//go:build darwin

package input

import "testing"

func TestVKToKeyCode(t *testing.T) {
	tests := []struct {
		name string
		vk   int32
		want uint16
	}{
		{"VK_A", 0x41, 0x00},        // kVK_ANSI_A
		{"VK_Z", 0x5A, 0x06},        // kVK_ANSI_Z
		{"VK_0", 0x30, 0x1D},        // kVK_ANSI_0
		{"VK_9", 0x39, 0x19},        // kVK_ANSI_9
		{"VK_RETURN", 0x0D, 0x24},   // kVK_Return
		{"VK_ESCAPE", 0x1B, 0x35},   // kVK_Escape
		{"VK_SPACE", 0x20, 0x31},    // kVK_Space
		{"VK_TAB", 0x09, 0x30},      // kVK_Tab
		{"VK_BACK", 0x08, 0x33},     // kVK_Delete (backspace)
		{"VK_LSHIFT", 0xA0, 0x38},   // kVK_Shift
		{"VK_RSHIFT", 0xA1, 0x3C},   // kVK_RightShift
		{"VK_LCONTROL", 0xA2, 0x3B}, // kVK_Control
		{"VK_RCONTROL", 0xA3, 0x3E}, // kVK_RightControl
		{"VK_LMENU", 0xA4, 0x3A},    // kVK_Option
		{"VK_RMENU", 0xA5, 0x3D},    // kVK_RightOption
		{"VK_LWIN", 0x5B, 0x37},     // kVK_Command
		{"VK_F1", 0x70, 0x7A},       // kVK_F1
		{"VK_F12", 0x7B, 0x6F},      // kVK_F12
		{"VK_LEFT", 0x25, 0x7B},     // kVK_LeftArrow
		{"VK_UP", 0x26, 0x7E},       // kVK_UpArrow
		{"VK_RIGHT", 0x27, 0x7C},    // kVK_RightArrow
		{"VK_DOWN", 0x28, 0x7D},     // kVK_DownArrow
		{"VK_DELETE", 0x2E, 0x75},   // kVK_ForwardDelete
		{"VK_HOME", 0x24, 0x73},     // kVK_Home
		{"VK_END", 0x23, 0x77},      // kVK_End
		{"VK_PRIOR", 0x21, 0x74},    // kVK_PageUp
		{"VK_NEXT", 0x22, 0x79},     // kVK_PageDown
		{"unknown", 0xFFF, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := VKToKeyCode(tt.vk)
			if tt.want == 0 && tt.vk == 0xFFF {
				if ok {
					t.Errorf("expected unknown for VK 0x%X", tt.vk)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("VKToKeyCode(0x%X) = 0x%X, %v; want 0x%X, true", tt.vk, got, ok, tt.want)
			}
		})
	}
}
