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
