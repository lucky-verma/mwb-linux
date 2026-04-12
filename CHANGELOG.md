# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Changed
- CI: opt into Node.js 24 for GitHub Actions ahead of June 2026 forced migration.

## [0.3.1] - 2026-04-12

### Fixed
- **Bidirectional bounce-back**: Mouse no longer instantly snaps back to Ubuntu
  after crossing to the Windows screen. Virtual cursor now starts 200px inside
  the remote screen instead of at the exact return-edge threshold. Added
  `canReturn` gate — mirrors the existing `canSwitch` guard on the local side —
  requiring the cursor to move away from the return edge before switch-back can
  fire.
- **`canReturn` not reset on reconnect**: `SetActive(true)` now resets both
  `canSwitch` and `canReturn`, preventing ghost bounce-back from stale state
  after a disconnect/reconnect cycle.
- **DISPLAY/XAUTHORITY hardcoding**: Systemd service no longer requires a
  hardcoded `DISPLAY=` value. The binary auto-detects the active X11 display via
  `loginctl` session query, with `/tmp/.X11-unix` socket scan as fallback.
  XAUTHORITY is also auto-detected from `/run/user/*/gdm/`. Fixes edge detection
  silently failing after reboots or GDM updates where the display number shifts.
- **Screen resolution**: Correctly detected (e.g. 2560×1440 instead of 1920×1080
  fallback) because `DISPLAY` is now propagated to the process environment before
  `xrandr` runs.
- **Race conditions**: `cachedDisplay` package-level var guarded with `sync.Once`
  — `DetectDisplay()` is safe to call from concurrent goroutines. Hotkey state
  (`hotkeyCtrl`/`hotkeyAlt`) moved from package-level vars into `Capturer` struct.
  Key material cache in `network/client.go` guarded with `sync.Mutex`.

### Changed
- `packaging/mwb.service` synced with root `mwb.service`: correct binary path
  (`/usr/local/bin/mwb`), bidirectional flags, display auto-detection comments.

## [0.3.0] - 2026-03-25

### Added
- **Dual-mode connection**: mwb now listens for incoming connections from Windows
  AND simultaneously tries outbound connect — first one wins. Enables instant
  reconnect after Windows lock/unlock cycles instead of waiting ~16s for Windows
  to start listening again.
- **Proactive heartbeats**: Send `HeartbeatEx` every 5s to prevent Windows MWB
  from silently dropping the connection.
- **TCP keep-alive**: 10s interval prevents NAT/firewall timeouts on idle
  connections.
- **Faster reconnect**: Backoff reduced from 1s–30s to 100ms–10s.

### Changed
- **Instant edge switching**: Replaced the 2s debounce timer with a `canSwitch`
  gate — switch fires the moment the cursor hits the edge, not after a delay.
- **Y-position matching**: Cursor enters the remote screen at a proportionally
  matched Y coordinate instead of screen center.
- **Correct entry edge**: Cursor enters from the right edge of Windows when
  coming from the left edge of Ubuntu (was entering from center).
- **Mouse acceleration**: 2× multiplier applied to evdev deltas for natural
  remote cursor movement speed.
- **Polling rate**: Increased from 50ms to 10ms for more responsive edge
  detection.
- **Grace period**: Reduced from 500ms to 100ms for faster transitions.
- **PBKDF2 key derivation cached** across reconnects (50k iterations is
  expensive — now only runs once per security key).

### Fixed
- Freeze on return: synchronous `xinput disable/enable` + cursor reposition
  prevents Ubuntu cursor from moving during Windows control.
- Edge trigger zone widened to 5px for more reliable activation.
- CI lint errors resolved; macOS removed from test matrix (Linux-only project).

## [0.1.0] - 2026-03-24

### Added
- Initial public release: native Linux client for Microsoft PowerToys Mouse
  Without Borders.
- Bidirectional mouse, keyboard, and clipboard sharing between Linux and Windows.
- AES-256-CBC encrypted protocol, fully compatible with PowerToys MWB.
- Device isolation via `xinput disable/enable` to prevent local cursor movement
  while controlling Windows.
- Text and image clipboard sync both directions via `xclip`/`xsel`.
- Ctrl+Alt+Right hotkey to force-return to Ubuntu if stuck.
- Systemd user service with auto-restart.
- `scripts/install.sh` one-command installer.
- GitHub Actions CI/CD: automated test, lint, and `.deb` release pipeline.

[Unreleased]: https://github.com/lucky-verma/mwb-linux/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/lucky-verma/mwb-linux/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/lucky-verma/mwb-linux/compare/v0.1.0...v0.3.0
[0.1.0]: https://github.com/lucky-verma/mwb-linux/releases/tag/v0.1.0
