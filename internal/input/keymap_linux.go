//go:build linux

// internal/input/keymap_linux.go
package input

// Linux evdev key codes (from /usr/include/linux/input-event-codes.h)
const (
	KEY_ESC        uint16 = 1
	KEY_1          uint16 = 2
	KEY_2          uint16 = 3
	KEY_3          uint16 = 4
	KEY_4          uint16 = 5
	KEY_5          uint16 = 6
	KEY_6          uint16 = 7
	KEY_7          uint16 = 8
	KEY_8          uint16 = 9
	KEY_9          uint16 = 10
	KEY_0          uint16 = 11
	KEY_MINUS      uint16 = 12
	KEY_EQUAL      uint16 = 13
	KEY_BACKSPACE  uint16 = 14
	KEY_TAB        uint16 = 15
	KEY_Q          uint16 = 16
	KEY_W          uint16 = 17
	KEY_E          uint16 = 18
	KEY_R          uint16 = 19
	KEY_T          uint16 = 20
	KEY_Y          uint16 = 21
	KEY_U          uint16 = 22
	KEY_I          uint16 = 23
	KEY_O          uint16 = 24
	KEY_P          uint16 = 25
	KEY_LEFTBRACE  uint16 = 26
	KEY_RIGHTBRACE uint16 = 27
	KEY_ENTER      uint16 = 28
	KEY_LEFTCTRL   uint16 = 29
	KEY_A          uint16 = 30
	KEY_S          uint16 = 31
	KEY_D          uint16 = 32
	KEY_F          uint16 = 33
	KEY_G          uint16 = 34
	KEY_H          uint16 = 35
	KEY_J          uint16 = 36
	KEY_K          uint16 = 37
	KEY_L          uint16 = 38
	KEY_SEMICOLON  uint16 = 39
	KEY_APOSTROPHE uint16 = 40
	KEY_GRAVE      uint16 = 41
	KEY_LEFTSHIFT  uint16 = 42
	KEY_BACKSLASH  uint16 = 43
	KEY_Z          uint16 = 44
	KEY_X          uint16 = 45
	KEY_C          uint16 = 46
	KEY_V          uint16 = 47
	KEY_B          uint16 = 48
	KEY_N          uint16 = 49
	KEY_M          uint16 = 50
	KEY_COMMA      uint16 = 51
	KEY_DOT        uint16 = 52
	KEY_SLASH      uint16 = 53
	KEY_RIGHTSHIFT uint16 = 54
	KEY_KPASTERISK uint16 = 55
	KEY_LEFTALT    uint16 = 56
	KEY_SPACE      uint16 = 57
	KEY_CAPSLOCK   uint16 = 58
	KEY_F1         uint16 = 59
	KEY_F2         uint16 = 60
	KEY_F3         uint16 = 61
	KEY_F4         uint16 = 62
	KEY_F5         uint16 = 63
	KEY_F6         uint16 = 64
	KEY_F7         uint16 = 65
	KEY_F8         uint16 = 66
	KEY_F9         uint16 = 67
	KEY_F10        uint16 = 68
	KEY_NUMLOCK    uint16 = 69
	KEY_SCROLLLOCK uint16 = 70
	KEY_KP7        uint16 = 71
	KEY_KP8        uint16 = 72
	KEY_KP9        uint16 = 73
	KEY_KPMINUS    uint16 = 74
	KEY_KP4        uint16 = 75
	KEY_KP5        uint16 = 76
	KEY_KP6        uint16 = 77
	KEY_KPPLUS     uint16 = 78
	KEY_KP1        uint16 = 79
	KEY_KP2        uint16 = 80
	KEY_KP3        uint16 = 81
	KEY_KP0        uint16 = 82
	KEY_KPDOT      uint16 = 83
	KEY_F11        uint16 = 87
	KEY_F12        uint16 = 88
	KEY_KPENTER    uint16 = 96
	KEY_RIGHTCTRL  uint16 = 97
	KEY_KPSLASH    uint16 = 98
	KEY_SYSRQ      uint16 = 99
	KEY_RIGHTALT   uint16 = 100
	KEY_HOME       uint16 = 102
	KEY_UP         uint16 = 103
	KEY_PAGEUP     uint16 = 104
	KEY_LEFT       uint16 = 105
	KEY_RIGHT      uint16 = 106
	KEY_END        uint16 = 107
	KEY_DOWN       uint16 = 108
	KEY_PAGEDOWN   uint16 = 109
	KEY_INSERT     uint16 = 110
	KEY_DELETE     uint16 = 111
	KEY_MUTE       uint16 = 113
	KEY_VOLUMEDOWN uint16 = 114
	KEY_VOLUMEUP   uint16 = 115
	KEY_PAUSE      uint16 = 119
	KEY_LEFTMETA   uint16 = 125
	KEY_RIGHTMETA  uint16 = 126
	KEY_COMPOSE    uint16 = 127
	KEY_102ND      uint16 = 86
	KEY_MAX        uint16 = 0x2ff
)

const (
	vkA        int32 = 0x41
	vkM        int32 = 0x4D
	vkQ        int32 = 0x51
	vkW        int32 = 0x57
	vkY        int32 = 0x59
	vkZ        int32 = 0x5A
	vkOEM1     int32 = 0xBA
	vkOEMPlus  int32 = 0xBB
	vkOEMComma int32 = 0xBC
	vkOEMMinus int32 = 0xBD
	vkOEMDot   int32 = 0xBE
	vkOEM2     int32 = 0xBF
	vkOEM3     int32 = 0xC0
	vkOEM4     int32 = 0xDB
	vkOEM5     int32 = 0xDC
	vkOEM6     int32 = 0xDD
	vkOEM7     int32 = 0xDE
	vkOEM8     int32 = 0xDF
	vkOEM102   int32 = 0xE2
)

// vkMap maps Windows VK codes to Linux evdev KEY_ codes.
var vkMap = map[int32]uint16{
	0x08: KEY_BACKSPACE, 0x09: KEY_TAB, 0x0D: KEY_ENTER,
	0x13: KEY_PAUSE, 0x14: KEY_CAPSLOCK, 0x1B: KEY_ESC, 0x20: KEY_SPACE,
	0x21: KEY_PAGEUP, 0x22: KEY_PAGEDOWN, 0x23: KEY_END, 0x24: KEY_HOME,
	0x25: KEY_LEFT, 0x26: KEY_UP, 0x27: KEY_RIGHT, 0x28: KEY_DOWN,
	0x2C: KEY_SYSRQ, 0x2D: KEY_INSERT, 0x2E: KEY_DELETE,
	0x5B: KEY_LEFTMETA, 0x5C: KEY_RIGHTMETA, 0x5D: KEY_COMPOSE,
	0x60: KEY_KP0, 0x61: KEY_KP1, 0x62: KEY_KP2, 0x63: KEY_KP3,
	0x64: KEY_KP4, 0x65: KEY_KP5, 0x66: KEY_KP6, 0x67: KEY_KP7,
	0x68: KEY_KP8, 0x69: KEY_KP9,
	0x6A: KEY_KPASTERISK, 0x6B: KEY_KPPLUS, 0x6D: KEY_KPMINUS,
	0x6E: KEY_KPDOT, 0x6F: KEY_KPSLASH,
	0x70: KEY_F1, 0x71: KEY_F2, 0x72: KEY_F3, 0x73: KEY_F4,
	0x74: KEY_F5, 0x75: KEY_F6, 0x76: KEY_F7, 0x77: KEY_F8,
	0x78: KEY_F9, 0x79: KEY_F10, 0x7A: KEY_F11, 0x7B: KEY_F12,
	0x90: KEY_NUMLOCK, 0x91: KEY_SCROLLLOCK,
	0xA0: KEY_LEFTSHIFT, 0xA1: KEY_RIGHTSHIFT,
	0xA2: KEY_LEFTCTRL, 0xA3: KEY_RIGHTCTRL,
	0xA4: KEY_LEFTALT, 0xA5: KEY_RIGHTALT,
	0xAD: KEY_MUTE, 0xAE: KEY_VOLUMEDOWN, 0xAF: KEY_VOLUMEUP,
	vkOEM1: KEY_SEMICOLON, vkOEMPlus: KEY_EQUAL, vkOEMComma: KEY_COMMA,
	vkOEMMinus: KEY_MINUS, vkOEMDot: KEY_DOT, vkOEM2: KEY_SLASH,
	vkOEM3: KEY_GRAVE, vkOEM4: KEY_LEFTBRACE, vkOEM5: KEY_BACKSLASH,
	vkOEM6: KEY_RIGHTBRACE, vkOEM7: KEY_APOSTROPHE,
	vkOEM102: KEY_102ND, // common on ISO keyboards (< > |)
}

var layoutVKOverrides = map[string]map[int32]uint16{
	"de": profile(
		key(vkY, KEY_Z), key(vkZ, KEY_Y),
		key(vkOEM1, KEY_LEFTBRACE), key(vkOEMPlus, KEY_RIGHTBRACE), key(vkOEMMinus, KEY_SLASH),
		key(vkOEM3, KEY_SEMICOLON), key(vkOEM4, KEY_MINUS), key(vkOEM5, KEY_GRAVE),
		key(vkOEM6, KEY_EQUAL), key(vkOEM7, KEY_APOSTROPHE), key(vkOEM2, KEY_BACKSLASH),
		key(vkOEM102, KEY_102ND),
	),
	"fr": profile(
		key(vkA, KEY_Q), key(vkQ, KEY_A), key(vkZ, KEY_W), key(vkW, KEY_Z), key(vkM, KEY_SEMICOLON),
		key(vkOEM7, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEMPlus, KEY_EQUAL),
		key(vkOEM6, KEY_LEFTBRACE), key(vkOEM1, KEY_RIGHTBRACE), key(vkOEM3, KEY_APOSTROPHE),
		key(vkOEM5, KEY_BACKSLASH), key(vkOEMComma, KEY_M), key(vkOEMDot, KEY_COMMA),
		key(vkOEM2, KEY_DOT), key(vkOEM8, KEY_SLASH), key(vkOEM102, KEY_102ND),
	),
	"be": profile(
		key(vkA, KEY_Q), key(vkQ, KEY_A), key(vkZ, KEY_W), key(vkW, KEY_Z), key(vkM, KEY_SEMICOLON),
		key(vkOEM7, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEMMinus, KEY_EQUAL),
		key(vkOEM6, KEY_LEFTBRACE), key(vkOEM1, KEY_RIGHTBRACE), key(vkOEM3, KEY_APOSTROPHE),
		key(vkOEM5, KEY_BACKSLASH), key(vkOEMDot, KEY_COMMA), key(vkOEM2, KEY_DOT),
		key(vkOEMPlus, KEY_SLASH), key(vkOEMComma, KEY_M), key(vkOEM102, KEY_102ND),
	),
	"es": profile(
		key(vkOEM5, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEM6, KEY_EQUAL),
		key(vkOEM1, KEY_LEFTBRACE), key(vkOEMPlus, KEY_RIGHTBRACE),
		key(vkOEM3, KEY_SEMICOLON), key(vkOEM7, KEY_APOSTROPHE),
		key(vkOEM2, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
	"it": profile(
		key(vkOEM5, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEM6, KEY_EQUAL),
		key(vkOEM1, KEY_LEFTBRACE), key(vkOEMPlus, KEY_RIGHTBRACE),
		key(vkOEM3, KEY_SEMICOLON), key(vkOEM7, KEY_APOSTROPHE),
		key(vkOEM2, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
	"gb": profile(
		key(vkOEM8, KEY_GRAVE), key(vkOEM3, KEY_APOSTROPHE), key(vkOEM7, KEY_BACKSLASH),
		key(vkOEM5, KEY_102ND), key(vkOEM102, KEY_102ND),
	),
	"pt": profile(
		key(vkOEM5, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEM6, KEY_EQUAL),
		key(vkOEMPlus, KEY_LEFTBRACE), key(vkOEM1, KEY_RIGHTBRACE),
		key(vkOEM3, KEY_SEMICOLON), key(vkOEM7, KEY_APOSTROPHE),
		key(vkOEM2, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
	"nordic": profile(
		key(vkOEM5, KEY_GRAVE), key(vkOEMPlus, KEY_MINUS), key(vkOEM4, KEY_EQUAL),
		key(vkOEM6, KEY_LEFTBRACE), key(vkOEM1, KEY_RIGHTBRACE),
		key(vkOEM3, KEY_SEMICOLON), key(vkOEM7, KEY_APOSTROPHE),
		key(vkOEM2, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
	"ch": profile(
		key(vkY, KEY_Z), key(vkZ, KEY_Y),
		key(vkOEM2, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEM6, KEY_EQUAL),
		key(vkOEM1, KEY_LEFTBRACE), key(vkOEM3, KEY_RIGHTBRACE),
		key(vkOEM7, KEY_SEMICOLON), key(vkOEM5, KEY_APOSTROPHE),
		key(vkOEM8, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
	"nl": profile(
		key(vkOEM7, KEY_GRAVE), key(vkOEM4, KEY_MINUS), key(vkOEM2, KEY_EQUAL),
		key(vkOEM6, KEY_LEFTBRACE), key(vkOEM1, KEY_RIGHTBRACE),
		key(vkOEMPlus, KEY_SEMICOLON), key(vkOEM3, KEY_APOSTROPHE),
		key(vkOEM5, KEY_BACKSLASH), key(vkOEM102, KEY_102ND),
	),
}

type vkKey struct {
	vk  int32
	key uint16
}

func key(vk int32, code uint16) vkKey {
	return vkKey{vk: vk, key: code}
}

func profile(pairs ...vkKey) map[int32]uint16 {
	out := make(map[int32]uint16, len(pairs))
	for _, pair := range pairs {
		out[pair.vk] = pair.key
	}
	return out
}

func init() {
	// A-Z: VK 0x41-0x5A
	letters := []uint16{
		KEY_A, KEY_B, KEY_C, KEY_D, KEY_E, KEY_F, KEY_G, KEY_H, KEY_I,
		KEY_J, KEY_K, KEY_L, KEY_M, KEY_N, KEY_O, KEY_P, KEY_Q, KEY_R,
		KEY_S, KEY_T, KEY_U, KEY_V, KEY_W, KEY_X, KEY_Y, KEY_Z,
	}
	for vk := int32(0x41); vk <= 0x5A; vk++ {
		vkMap[vk] = letters[vk-0x41]
	}
	// 0-9: VK 0x30-0x39
	digits := []uint16{KEY_0, KEY_1, KEY_2, KEY_3, KEY_4, KEY_5, KEY_6, KEY_7, KEY_8, KEY_9}
	for vk := int32(0x30); vk <= 0x39; vk++ {
		vkMap[vk] = digits[vk-0x30]
	}
}

// VKToKeyCode maps a Windows Virtual Key code to a platform-specific key code.
func VKToKeyCode(vk int32) (uint16, bool) {
	code, ok := vkMap[vk]
	return code, ok
}

// VKToKeyCodeForLayout maps a Windows virtual-key code to the Linux evdev key
// that produces the same logical key on the requested keyboard layout.
func VKToKeyCodeForLayout(vk int32, layout string) (uint16, bool) {
	if overrides, ok := normalizedLayoutOverrides(layout); ok {
		if code, ok := overrides[vk]; ok {
			return code, true
		}
	}
	return VKToKeyCode(vk)
}

func normalizedLayoutOverrides(layout string) (map[int32]uint16, bool) {
	profile := CanonicalKeyboardLayout(layout)
	overrides, ok := layoutVKOverrides[profile]
	return overrides, ok
}

// usedKeyCodes returns a deduplicated sorted list of all Linux evdev key codes
// referenced by vkMap. Used by CreateVirtualKeyboard to register only the bits
// it needs — avoids 640+ unnecessary UI_SET_KEYBIT ioctl calls (KEY_MAX=767
// vs actual max used code of ~127).
func usedKeyCodes() []uint16 {
	seen := make(map[uint16]struct{}, len(vkMap))
	for _, code := range vkMap {
		seen[code] = struct{}{}
	}
	codes := make([]uint16, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	// Sort for deterministic ioctl order
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j] < codes[j-1]; j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
	return codes
}
