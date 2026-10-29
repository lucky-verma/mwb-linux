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

func TestVKToKeyCodeForGermanLayout(t *testing.T) {
	tests := []struct {
		name string
		vk   int32
		want uint16
	}{
		{"VK_Z", 0x5A, KEY_Y},
		{"VK_Y", 0x59, KEY_Z},
		{"VK_OEM_4_ssharp", 0xDB, KEY_MINUS},
		{"VK_OEM_1_udiaeresis", 0xBA, KEY_LEFTBRACE},
		{"VK_OEM_3_odiaeresis", 0xC0, KEY_SEMICOLON},
		{"VK_OEM_7_adiaeresis", 0xDE, KEY_APOSTROPHE},
		{"VK_OEM_PLUS", 0xBB, KEY_RIGHTBRACE},
		{"VK_OEM_6_dead_acute", 0xDD, KEY_EQUAL},
		{"VK_OEM_5_dead_circumflex", 0xDC, KEY_GRAVE},
		{"VK_OEM_2_numbersign", 0xBF, KEY_BACKSLASH},
		{"VK_OEM_MINUS", 0xBD, KEY_SLASH},
		{"VK_OEM_102", 0xE2, KEY_102ND},
		{"VK_2_stays_physical", 0x32, KEY_2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := VKToKeyCodeForLayout(tt.vk, "de")
			if !ok || got != tt.want {
				t.Errorf("VKToKeyCodeForLayout(0x%X, de) = %d, %v; want %d, true", tt.vk, got, ok, tt.want)
			}
		})
	}
}

func TestVKToKeyCodeForUnknownLayoutFallsBack(t *testing.T) {
	got, ok := VKToKeyCodeForLayout(0x5A, "unsupported")
	if !ok || got != KEY_Z {
		t.Errorf("VKToKeyCodeForLayout(0x5A, unsupported) = %d, %v; want %d, true", got, ok, KEY_Z)
	}
}

func TestVKToKeyCodeForCommonLayoutProfiles(t *testing.T) {
	tests := []struct {
		layout string
		vk     int32
		want   uint16
	}{
		{"fr", vkA, KEY_Q},
		{"fr", vkQ, KEY_A},
		{"fr", vkM, KEY_SEMICOLON},
		{"fr", vkOEMComma, KEY_M},
		{"be", vkOEMPlus, KEY_SLASH},
		{"es", vkOEM1, KEY_LEFTBRACE},
		{"es", vkOEMPlus, KEY_RIGHTBRACE},
		{"it", vkOEM4, KEY_MINUS},
		{"gb", vkOEM7, KEY_BACKSLASH},
		{"gb", vkOEM5, KEY_102ND},
		{"pt", vkOEMPlus, KEY_LEFTBRACE},
		{"nordic", vkOEMPlus, KEY_MINUS},
		{"nordic", vkOEM4, KEY_EQUAL},
		{"ch", vkZ, KEY_Y},
		{"ch", vkOEM8, KEY_BACKSLASH},
		{"nl", vkOEMPlus, KEY_SEMICOLON},
	}
	for _, tt := range tests {
		t.Run(tt.layout, func(t *testing.T) {
			got, ok := VKToKeyCodeForLayout(tt.vk, tt.layout)
			if !ok || got != tt.want {
				t.Errorf("VKToKeyCodeForLayout(0x%X, %s) = %d, %v; want %d, true", tt.vk, tt.layout, got, ok, tt.want)
			}
		})
	}
}

func TestCanonicalKeyboardLayout(t *testing.T) {
	tests := map[string]string{
		"":               "",
		"auto":           "auto",
		"de":             "de",
		"de_DE":          "de",
		"de,us":          "de",
		"German":         "de",
		"fr-FR":          "fr",
		"Belgian":        "be",
		"Spanish":        "es",
		"Latin-American": "es",
		"it-IT":          "it",
		"en-GB":          "gb",
		"Portuguese":     "pt",
		"Norwegian":      "nordic",
		"Danish":         "nordic",
		"Swedish":        "nordic",
		"Finnish":        "nordic",
		"Swiss-German":   "ch",
		"Dutch":          "nl",
		"en-US":          "us",
		"us":             "us",
	}
	for input, want := range tests {
		if got := CanonicalKeyboardLayout(input); got != want {
			t.Errorf("CanonicalKeyboardLayout(%q) = %q, want %q", input, got, want)
		}
	}
}

// Two mwb-linux peers encode keys with KeyCodeToVK and decode them with the
// positional "us" profile. Every key the sender can encode must arrive as the
// same physical key, or typing on one Linux machine produces the wrong key on
// the other.
func TestLinuxToLinuxKeysRoundTrip(t *testing.T) {
	checked := 0
	for code := uint16(1); code < 0x300; code++ {
		vk, ok := KeyCodeToVK(code)
		if !ok {
			continue
		}
		back, ok := VKToKeyCodeForLayout(vk, "us")
		if !ok || back != code {
			t.Errorf("evdev %d -> VK 0x%X -> evdev %d (ok=%v)", code, vk, back, ok)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no keys checked")
	}
}

// Media, browser and launch keys and F13-F24 cross between machines, and the
// virtual keyboard can type them.
func TestExtraKeysMapBothWays(t *testing.T) {
	keys := map[int32]uint16{
		0xB3: KEY_PLAYPAUSE, 0xB0: KEY_NEXTSONG, 0xB1: KEY_PREVIOUSSONG, 0xB2: KEY_STOPCD,
		0xA6: KEY_BACK, 0xA7: KEY_FORWARD, 0xAA: KEY_SEARCH, 0xAC: KEY_HOMEPAGE,
		0xB4: KEY_MAIL, 0xB7: KEY_CALC, 0x5F: KEY_SLEEP,
		0x7C: KEY_F13, 0x87: KEY_F24,
	}
	used := map[uint16]bool{}
	for _, code := range usedKeyCodes() {
		used[code] = true
	}
	for vk, code := range keys {
		if got, ok := VKToKeyCode(vk); !ok || got != code {
			t.Errorf("VK 0x%X -> %d (ok=%v), want %d", vk, got, ok, code)
		}
		if got, ok := KeyCodeToVK(code); !ok || got != vk {
			t.Errorf("evdev %d -> VK 0x%X (ok=%v), want 0x%X", code, got, ok, vk)
		}
		if !used[code] {
			t.Errorf("virtual keyboard does not register evdev %d", code)
		}
	}
}

// Keypad Enter travels as an extended VK_RETURN and comes back as itself.
func TestKeypadEnterRoundTrip(t *testing.T) {
	vk, extended, ok := KeyCodeToVKExtended(KEY_KPENTER)
	if !ok || vk != 0x0D || !extended {
		t.Fatalf("KP Enter -> VK 0x%X extended=%v ok=%v", vk, extended, ok)
	}
	if code, ok := ExtendedKeyCode(vk); !ok || code != KEY_KPENTER {
		t.Fatalf("extended VK_RETURN -> %d", code)
	}
	if _, extended, _ := KeyCodeToVKExtended(KEY_ENTER); extended {
		t.Fatal("the main Enter key was marked extended")
	}
	found := false
	for _, code := range usedKeyCodes() {
		found = found || code == KEY_KPENTER
	}
	if !found {
		t.Fatal("virtual keyboard does not register KP Enter")
	}
}
