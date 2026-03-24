//go:build linux

// internal/input/keymap_linux_test.go
package input

import "testing"

func TestVKToKeyCode(t *testing.T) {
	tests := []struct {
		name string
		vk   int32
		want uint16
	}{
		{"VK_A", 0x41, KEY_A},
		{"VK_Z", 0x5A, KEY_Z},
		{"VK_0", 0x30, KEY_0},
		{"VK_9", 0x39, KEY_9},
		{"VK_RETURN", 0x0D, KEY_ENTER},
		{"VK_ESCAPE", 0x1B, KEY_ESC},
		{"VK_SPACE", 0x20, KEY_SPACE},
		{"VK_TAB", 0x09, KEY_TAB},
		{"VK_BACK", 0x08, KEY_BACKSPACE},
		{"VK_LSHIFT", 0xA0, KEY_LEFTSHIFT},
		{"VK_RSHIFT", 0xA1, KEY_RIGHTSHIFT},
		{"VK_LCONTROL", 0xA2, KEY_LEFTCTRL},
		{"VK_RCONTROL", 0xA3, KEY_RIGHTCTRL},
		{"VK_LMENU", 0xA4, KEY_LEFTALT},
		{"VK_RMENU", 0xA5, KEY_RIGHTALT},
		{"VK_LWIN", 0x5B, KEY_LEFTMETA},
		{"VK_F1", 0x70, KEY_F1},
		{"VK_F12", 0x7B, KEY_F12},
		{"VK_LEFT", 0x25, KEY_LEFT},
		{"VK_UP", 0x26, KEY_UP},
		{"VK_RIGHT", 0x27, KEY_RIGHT},
		{"VK_DOWN", 0x28, KEY_DOWN},
		{"VK_DELETE", 0x2E, KEY_DELETE},
		{"VK_INSERT", 0x2D, KEY_INSERT},
		{"VK_HOME", 0x24, KEY_HOME},
		{"VK_END", 0x23, KEY_END},
		{"VK_PRIOR", 0x21, KEY_PAGEUP},
		{"VK_NEXT", 0x22, KEY_PAGEDOWN},
		{"unknown", 0xFFF, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := VKToKeyCode(tt.vk)
			if tt.want == 0 {
				if ok {
					t.Errorf("expected unknown for VK 0x%X", tt.vk)
				}
				return
			}
			if !ok || got != tt.want {
				t.Errorf("VKToKeyCode(0x%X) = %d, %v; want %d, true", tt.vk, got, ok, tt.want)
			}
		})
	}
}
