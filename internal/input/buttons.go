package input

// Button codes passed to MouseDevice.ButtonDown/ButtonUp.
// Values match Linux evdev codes for backward compatibility.
const (
	BTN_LEFT   uint16 = 0x110
	BTN_RIGHT  uint16 = 0x111
	BTN_MIDDLE uint16 = 0x112
)
