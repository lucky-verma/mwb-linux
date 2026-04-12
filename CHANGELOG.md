# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.3.1] - 2026-04-11

### Fixed
- **Bidirectional bounce-back**: Mouse no longer instantly snaps back to Ubuntu
  after crossing to the Windows screen. Virtual cursor now starts 200px inside
  the remote screen instead of at the exact return-edge threshold. Added
  `canReturn` gate — mirrors the existing `canSwitch` guard on the local side —
  requiring the cursor to move away from the return edge before switch-back can
  fire.
- **`canReturn` not reset on reconnect**: `SetActive(true)` now resets both
  `canSwitch` and `canReturn`, preventing ghost bounces from stale state after
  a disconnect/reconnect cycle.
- **DISPLAY/XAUTHORITY hardcoding**: Systemd service no longer requires a
  hardcoded `DISPLAY=` value. The binary auto-detects the active X11 display via
  `loginctl` session query, with `/tmp/.X11-unix` socket scan as fallback.
  XAUTHORITY is also auto-detected from `/run/user/*/gdm/`. Fixes edge detection
  silently failing after reboots or GDM updates where the display number shifts.
- Screen resolution now correctly detected (e.g. 2560×1440 instead of 1920×1080
  fallback) because `DISPLAY` is propagated to the process environment before
  `xrandr` runs.
- **Race conditions**: `cachedDisplay` package-level var is now guarded by
  `sync.Once` — safe to call `DetectDisplay()` from concurrent goroutines.
  Hotkey state (`hotkeyCtrl`/`hotkeyAlt`) moved from package-level vars into the
  `Capturer` struct. Key material cache in `network/client.go` guarded with
  `sync.Mutex`.

### Changed
- `packaging/mwb.service` synced with root `mwb.service` (correct binary path,
  bidirectional flags, display auto-detection comments).

## [0.3.0] - 2026-03-25

### Added
- Initial release: native Linux client for Microsoft PowerToys Mouse Without Borders.
- Bidirectional mouse, keyboard, and clipboard sharing between Linux and Windows.
- AES-256-CBC encrypted protocol, fully compatible with PowerToys MWB.
- Edge detection via `xdotool` cursor polling (10ms interval).
- `canSwitch` gate: cursor must move 20px away from local edge before re-arming.
- Synchronous `xdotool mousemove` + `xinput disable/enable` for clean transitions.
- 5-packet mouse burst on switch for reliable Windows MWB registration.
- Y-position proportional mapping between screens on switch.
- Clipboard sync (text + images) both directions via `xclip`/`xsel`.
- Ctrl+Alt+Right hotkey to force-return to Ubuntu if stuck.
- Systemd user service with auto-restart.
- `scripts/install.sh` one-command installer.
- `.deb` package built and published on tagged releases.
