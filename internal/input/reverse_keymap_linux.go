//go:build linux

package input

// reverseVKMap maps Linux evdev KEY_ codes back to Windows VK codes.
var reverseVKMap map[uint16]int32

func init() {
	// Force vkMap to be populated first (it has its own init)
	reverseVKMap = make(map[uint16]int32, len(vkMap))
	for vk, evdev := range vkMap {
		reverseVKMap[evdev] = vk
	}
}

// KeyCodeToVK maps a Linux evdev keycode to a Windows Virtual Key code.
func KeyCodeToVK(code uint16) (int32, bool) {
	vk, ok := reverseVKMap[code]
	return vk, ok
}

// KeyCodeToVKExtended is KeyCodeToVK plus whether the key must be sent with
// LLKHF_EXTENDED: keypad Enter shares VK_RETURN with the main Enter key.
func KeyCodeToVKExtended(code uint16) (vk int32, extended, ok bool) {
	if code == KEY_KPENTER {
		return vkReturn, true, true
	}
	vk, ok = KeyCodeToVK(code)
	return vk, false, ok
}

// ExtendedKeyCode is the key an extended VK stands for, when it differs from
// the plain one: keypad Enter for VK_RETURN.
func ExtendedKeyCode(vk int32) (uint16, bool) {
	if vk == vkReturn {
		return KEY_KPENTER, true
	}
	return 0, false
}
