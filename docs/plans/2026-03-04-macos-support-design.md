# macOS Support Design

## Goal

Add macOS as a supported platform for mwb, functioning as a receive-only client (same as the existing Linux client). The macOS client receives mouse and keyboard events from a Windows PowerToys MWB host and injects them locally.

## Approach

Direct CGo wrapper around Apple's CoreGraphics event APIs. No external dependencies.

### Why Direct CGo

- Only 3-4 C function calls needed (CGEventCreateMouseEvent, CGEventCreateKeyboardEvent, CGEventPost, CGEventCreateScrollWheelEvent2)
- Matches the project's minimal-dependency philosophy
- Full control over event posting behavior
- Alternatives (robotgo, keybd_event) are overkill or incomplete

## Architecture

The existing codebase already uses `MouseDevice` and `KeyboardDevice` interfaces in the handler, so network, protocol, and config code works unchanged. Only the `internal/input` package needs platform-specific implementations.

### File Changes

**New files:**

- `internal/input/buttons.go` — shared button constants (BTN_LEFT, BTN_RIGHT, BTN_MIDDLE), no build tag
- `internal/input/keymap_darwin.go` — `//go:build darwin`, Windows VK → macOS virtual keycode mapping + `VKToKeyCode()`
- `internal/input/coregraphics.go` — `//go:build darwin`, CGo implementation of VirtualMouse and VirtualKeyboard

**Modified files:**

- `internal/input/uinput.go` — remove BTN_* constants (moved to buttons.go)
- `internal/input/keymap.go` → rename to `internal/input/keymap_linux.go`, add `//go:build linux` tag, rename `VKToEvdev` → `VKToKeyCode`
- `internal/network/handler.go` — call `VKToKeyCode()` instead of `VKToEvdev()`
- `.goreleaser.yml` — add darwin to goos, enable CGo for darwin builds

**Unchanged:**

- `internal/network/` (all other files)
- `internal/protocol/`
- `internal/config/`
- `cmd/mwb/main.go`

## macOS Implementation Details

### Mouse (coregraphics.go)

VirtualMouse holds current screen dimensions, queried from `CGDisplayPixelsWide/High(CGMainDisplayID())` at creation time. Used to scale MWB's 0-65535 coordinate range to macOS pixel coordinates.

- `MoveTo(x, y)` — scale coords, `CGEventCreateMouseEvent(nil, kCGEventMouseMoved, point, 0)` + `CGEventPost`
- `ButtonDown(button)` — map BTN_LEFT/RIGHT/MIDDLE to kCGEventLeftMouseDown/kCGEventRightMouseDown/kCGEventOtherMouseDown, post at current position
- `ButtonUp(button)` — corresponding mouse-up events
- `Wheel(delta)` — `CGEventCreateScrollWheelEvent2(nil, kCGScrollEventUnitLine, 1, delta)` + `CGEventPost`
- `Close()` — no-op (no kernel resource to release)

### Keyboard (coregraphics.go)

- `KeyDown(code)` — `CGEventCreateKeyboardEvent(nil, code, true)` + `CGEventPost(kCGHIDEventTap, event)`
- `KeyUp(code)` — `CGEventCreateKeyboardEvent(nil, code, false)` + `CGEventPost`
- `Close()` — no-op

### Keymap (keymap_darwin.go)

Maps Windows VK codes to macOS virtual keycodes (kVK_ANSI_A = 0x00, kVK_Return = 0x24, etc.). Same structure as Linux keymap: a `vkMap` map and `VKToKeyCode(vk int32) (uint16, bool)` function.

## Build & Release

- `Makefile` — no changes; build tags handle platform selection
- `.goreleaser.yml` — add `darwin` to goos, set `CGO_ENABLED=1` for darwin (keep `0` for linux)
- CI — add macOS runner for testing

## macOS Permissions

macOS requires Accessibility permission for synthetic input injection. On first run, macOS prompts the user. The binary (or Terminal.app if run from terminal) must be added to System Settings > Privacy & Security > Accessibility.

Error messages will guide users to this setting if event posting fails.

## Config

Config path `~/.config/mwb/config.toml` works on macOS, no changes needed.

## Testing

- Keymap tests: pure Go, run on any platform
- CGo input tests: require macOS with Accessibility permissions, limited to CI with macOS runners
- Integration testing: manual (requires Windows MWB host), same as Linux

## Out of Scope

- Multi-monitor support (can be added later)
- Bidirectional support (macOS as sender)
- Clipboard sharing
