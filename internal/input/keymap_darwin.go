//go:build darwin

package input

// macOS virtual key codes (from Events.h / Carbon HIToolbox)
const (
	kVK_ANSI_A         uint16 = 0x00
	kVK_ANSI_S         uint16 = 0x01
	kVK_ANSI_D         uint16 = 0x02
	kVK_ANSI_F         uint16 = 0x03
	kVK_ANSI_H         uint16 = 0x04
	kVK_ANSI_G         uint16 = 0x05
	kVK_ANSI_Z         uint16 = 0x06
	kVK_ANSI_X         uint16 = 0x07
	kVK_ANSI_C         uint16 = 0x08
	kVK_ANSI_V         uint16 = 0x09
	kVK_ANSI_B         uint16 = 0x0B
	kVK_ANSI_Q         uint16 = 0x0C
	kVK_ANSI_W         uint16 = 0x0D
	kVK_ANSI_E         uint16 = 0x0E
	kVK_ANSI_R         uint16 = 0x0F
	kVK_ANSI_Y         uint16 = 0x10
	kVK_ANSI_T         uint16 = 0x11
	kVK_ANSI_1         uint16 = 0x12
	kVK_ANSI_2         uint16 = 0x13
	kVK_ANSI_3         uint16 = 0x14
	kVK_ANSI_4         uint16 = 0x15
	kVK_ANSI_6         uint16 = 0x16
	kVK_ANSI_5         uint16 = 0x17
	kVK_ANSI_Equal     uint16 = 0x18
	kVK_ANSI_9         uint16 = 0x19
	kVK_ANSI_7         uint16 = 0x1A
	kVK_ANSI_Minus     uint16 = 0x1B
	kVK_ANSI_8         uint16 = 0x1C
	kVK_ANSI_0         uint16 = 0x1D
	kVK_ANSI_RBracket  uint16 = 0x1E
	kVK_ANSI_O         uint16 = 0x1F
	kVK_ANSI_U         uint16 = 0x20
	kVK_ANSI_LBracket  uint16 = 0x21
	kVK_ANSI_I         uint16 = 0x22
	kVK_ANSI_P         uint16 = 0x23
	kVK_Return         uint16 = 0x24
	kVK_ANSI_L         uint16 = 0x25
	kVK_ANSI_J         uint16 = 0x26
	kVK_ANSI_Quote     uint16 = 0x27
	kVK_ANSI_K         uint16 = 0x28
	kVK_ANSI_Semicolon uint16 = 0x29
	kVK_ANSI_Backslash uint16 = 0x2A
	kVK_ANSI_Comma     uint16 = 0x2B
	kVK_ANSI_Slash     uint16 = 0x2C
	kVK_ANSI_N         uint16 = 0x2D
	kVK_ANSI_M         uint16 = 0x2E
	kVK_ANSI_Period    uint16 = 0x2F
	kVK_Tab            uint16 = 0x30
	kVK_Space          uint16 = 0x31
	kVK_ANSI_Grave     uint16 = 0x32
	kVK_Delete         uint16 = 0x33 // backspace
	kVK_Escape         uint16 = 0x35
	kVK_Command        uint16 = 0x37
	kVK_Shift          uint16 = 0x38
	kVK_CapsLock       uint16 = 0x39
	kVK_Option         uint16 = 0x3A
	kVK_Control        uint16 = 0x3B
	kVK_RightShift     uint16 = 0x3C
	kVK_RightOption    uint16 = 0x3D
	kVK_RightControl   uint16 = 0x3E

	kVK_ANSI_KpDecimal  uint16 = 0x41
	kVK_ANSI_KpMultiply uint16 = 0x43
	kVK_ANSI_KpPlus     uint16 = 0x45
	kVK_ANSI_KpClear    uint16 = 0x47
	kVK_VolumeUp        uint16 = 0x48
	kVK_VolumeDown      uint16 = 0x49
	kVK_Mute            uint16 = 0x4A
	kVK_ANSI_KpDivide   uint16 = 0x4B
	kVK_ANSI_KpEnter    uint16 = 0x4C
	kVK_ANSI_KpMinus    uint16 = 0x4E
	kVK_ANSI_Keypad0    uint16 = 0x52
	kVK_ANSI_Keypad1    uint16 = 0x53
	kVK_ANSI_Keypad2    uint16 = 0x54
	kVK_ANSI_Keypad3    uint16 = 0x55
	kVK_ANSI_Keypad4    uint16 = 0x56
	kVK_ANSI_Keypad5    uint16 = 0x57
	kVK_ANSI_Keypad6    uint16 = 0x58
	kVK_ANSI_Keypad7    uint16 = 0x59
	kVK_ANSI_Keypad8    uint16 = 0x5A
	kVK_ANSI_Keypad9    uint16 = 0x5B

	kVK_F1            uint16 = 0x7A
	kVK_F2            uint16 = 0x78
	kVK_F3            uint16 = 0x63
	kVK_F4            uint16 = 0x76
	kVK_F5            uint16 = 0x60
	kVK_F6            uint16 = 0x61
	kVK_F7            uint16 = 0x62
	kVK_F8            uint16 = 0x64
	kVK_F9            uint16 = 0x65
	kVK_F10           uint16 = 0x6D
	kVK_F11           uint16 = 0x67
	kVK_F12           uint16 = 0x6F
	kVK_Help          uint16 = 0x72
	kVK_Home          uint16 = 0x73
	kVK_PageUp        uint16 = 0x74
	kVK_ForwardDelete uint16 = 0x75
	kVK_End           uint16 = 0x77
	kVK_PageDown      uint16 = 0x79
	kVK_LeftArrow     uint16 = 0x7B
	kVK_RightArrow    uint16 = 0x7C
	kVK_DownArrow     uint16 = 0x7D
	kVK_UpArrow       uint16 = 0x7E
)

// vkMap maps Windows VK codes to macOS virtual key codes.
var vkMap = map[int32]uint16{
	0x08: kVK_Delete, 0x09: kVK_Tab, 0x0D: kVK_Return,
	0x14: kVK_CapsLock, 0x1B: kVK_Escape, 0x20: kVK_Space,
	0x21: kVK_PageUp, 0x22: kVK_PageDown, 0x23: kVK_End, 0x24: kVK_Home,
	0x25: kVK_LeftArrow, 0x26: kVK_UpArrow, 0x27: kVK_RightArrow, 0x28: kVK_DownArrow,
	0x2D: kVK_Help, 0x2E: kVK_ForwardDelete,
	0x5B: kVK_Command, 0x5C: kVK_Command, // both Win keys map to Command
	0x60: kVK_ANSI_Keypad0, 0x61: kVK_ANSI_Keypad1, 0x62: kVK_ANSI_Keypad2, 0x63: kVK_ANSI_Keypad3,
	0x64: kVK_ANSI_Keypad4, 0x65: kVK_ANSI_Keypad5, 0x66: kVK_ANSI_Keypad6, 0x67: kVK_ANSI_Keypad7,
	0x68: kVK_ANSI_Keypad8, 0x69: kVK_ANSI_Keypad9,
	0x6A: kVK_ANSI_KpMultiply, 0x6B: kVK_ANSI_KpPlus, 0x6D: kVK_ANSI_KpMinus,
	0x6E: kVK_ANSI_KpDecimal, 0x6F: kVK_ANSI_KpDivide,
	0x70: kVK_F1, 0x71: kVK_F2, 0x72: kVK_F3, 0x73: kVK_F4,
	0x74: kVK_F5, 0x75: kVK_F6, 0x76: kVK_F7, 0x77: kVK_F8,
	0x78: kVK_F9, 0x79: kVK_F10, 0x7A: kVK_F11, 0x7B: kVK_F12,
	0x90: kVK_ANSI_KpClear, // NumLock maps to Clear on macOS
	0xA0: kVK_Shift, 0xA1: kVK_RightShift,
	0xA2: kVK_Control, 0xA3: kVK_RightControl,
	0xA4: kVK_Option, 0xA5: kVK_RightOption,
	0xAD: kVK_Mute, 0xAE: kVK_VolumeDown, 0xAF: kVK_VolumeUp,
	0xBA: kVK_ANSI_Semicolon, 0xBB: kVK_ANSI_Equal, 0xBC: kVK_ANSI_Comma,
	0xBD: kVK_ANSI_Minus, 0xBE: kVK_ANSI_Period, 0xBF: kVK_ANSI_Slash,
	0xC0: kVK_ANSI_Grave, 0xDB: kVK_ANSI_LBracket, 0xDC: kVK_ANSI_Backslash,
	0xDD: kVK_ANSI_RBracket, 0xDE: kVK_ANSI_Quote,
}

func init() {
	// A-Z: VK 0x41-0x5A -> macOS keycodes (not sequential!)
	letters := []uint16{
		kVK_ANSI_A, kVK_ANSI_B, kVK_ANSI_C, kVK_ANSI_D, kVK_ANSI_E, kVK_ANSI_F,
		kVK_ANSI_G, kVK_ANSI_H, kVK_ANSI_I, kVK_ANSI_J, kVK_ANSI_K, kVK_ANSI_L,
		kVK_ANSI_M, kVK_ANSI_N, kVK_ANSI_O, kVK_ANSI_P, kVK_ANSI_Q, kVK_ANSI_R,
		kVK_ANSI_S, kVK_ANSI_T, kVK_ANSI_U, kVK_ANSI_V, kVK_ANSI_W, kVK_ANSI_X,
		kVK_ANSI_Y, kVK_ANSI_Z,
	}
	for vk := int32(0x41); vk <= 0x5A; vk++ {
		vkMap[vk] = letters[vk-0x41]
	}
	// 0-9: VK 0x30-0x39 -> macOS keycodes (not sequential!)
	digits := []uint16{
		kVK_ANSI_0, kVK_ANSI_1, kVK_ANSI_2, kVK_ANSI_3, kVK_ANSI_4,
		kVK_ANSI_5, kVK_ANSI_6, kVK_ANSI_7, kVK_ANSI_8, kVK_ANSI_9,
	}
	for vk := int32(0x30); vk <= 0x39; vk++ {
		vkMap[vk] = digits[vk-0x30]
	}
}

// VKToKeyCode maps a Windows Virtual Key code to a macOS virtual key code.
func VKToKeyCode(vk int32) (uint16, bool) {
	code, ok := vkMap[vk]
	return code, ok
}
