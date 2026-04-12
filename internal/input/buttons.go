package input

// Button codes passed to MouseDevice.ButtonDown/ButtonUp.
// Values match Linux evdev codes for backward compatibility.
const (
	BTN_LEFT   uint16 = 0x110
	BTN_RIGHT  uint16 = 0x111
	BTN_MIDDLE uint16 = 0x112
	BTN_SIDE   uint16 = 0x113 // X-Button 1 (browser back)
	BTN_EXTRA  uint16 = 0x114 // X-Button 2 (browser forward)
)
