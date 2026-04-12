# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- X-button support (back/forward): `BTN_SIDE`/`BTN_EXTRA` registered on virtual
  mouse, `WM_XBUTTONDOWN`/`WM_XBUTTONUP` handled in input handler.
- Horizontal scroll: `REL_HWHEEL` registered, `WM_MOUSEHWHEEL` handled —
  two-finger horizontal swipe from Windows trackpad now works on Ubuntu.
- 15 regression tests in `internal/capture/` covering xinput state management,
  mutex invariants, edge gate logic, and floating slave filtering.
- PR template (`.github/PULL_REQUEST_TEMPLATE.md`) with build, race, lint, and
  xinput safety checklists.
- `docs/ARCHITECTURE.md`: documented 5 critical invariants with code examples
  and test references to prevent recurrence of known bug classes.

### Fixed
- **`SendPacket` data race**: `cipher.CBCEncrypter` is not goroutine-safe —
  concurrent calls from heartbeat, clipboard, and capture goroutines corrupted
  the AES stream. Added `sendMu sync.Mutex` to `Conn` serializing all writes.
- **Mouse button clicks at wrong position**: button events sent `X=0,Y=0` to
  Windows, registering every click at top-left. Now uses virtual cursor
  `remoteX/remoteY` state for correct click position.
- **`cfg.RemoteWidth/Height` ignored**: config values were parsed but never
  passed to `Capturer`, causing wrong virtual cursor mapping on non-1080p
  Windows displays and premature return-edge trigger.
- **`cfg.Edge` ignored**: `--edge` flag defaulted to `right`, silently
  overriding `edge = "left"` in `config.toml`. Now reads config if flag not set.
- **Deadlock after first edge switch**: `SetActive()` held `c.mu` and called
  `enableXinput()` which also acquires `c.mu` — Go mutexes are not reentrant.
  All goroutines waiting on `c.mu` froze permanently. Fixed by releasing lock
  before calling `enableXinput`.
- **Mouse/keyboard dead after `MachineSwitched`**: `OnActivated` callback did
  not move cursor away from the edge. Cursor stayed at `x=0`, any movement
  immediately re-triggered the edge switch. Added `xdotool mousemove` via
  `SafeEntryPosition()`, mirroring `OnReclaimed`.
- **Xinput floating slave corruption**: `enableXinput()` called unconditionally
  in `New()` and `Stop()` — calling `xinput enable` on `[floating slave]`
  devices corrupts attachment state, requiring manual `reattach`. Fixed: only
  call when `disabledXinputIDs` is non-empty.
- **Devices left disabled across sessions**: `enableXinput()` now merges cached
  IDs with a fresh scan to recover attached-but-disabled devices from prior
  broken sessions (e.g. connection drop mid-switch).
- **`monitorDevice` goroutine accumulation**: goroutines blocked on `f.Read()`
  indefinitely after `Stop()`. Fixed: track device fds in `Capturer`,
  close them in `Stop()`, wait on `WaitGroup`.
- **`sendText`/`sendImage` goroutines untracked**: clipboard send goroutines
  outlived the connection and wrote to closed conn. Tracked in `Manager.wg`.
- **Image clipboard echo-back**: `handleImageClipboard` set `justSet` but not
  `lastHash` — same image re-sent to Windows after 3s suppress window expired.
- **`parseXinputIDs` extracted** from `getXinputIDs` for testability; the
  critical `[floating slave]` filter is now covered by a regression test.
- **`uinput` keyboard init**: reduced from 767 ioctl calls to ~120 by only
  registering key codes present in the VK→evdev keymap.
- **Packet ID wraparound**: `nextID` now resets before reaching `0x7FFFFFFF`
  to avoid negative IDs violating protocol dedup requirements.

### Changed
- CI: opt into Node.js 24 for GitHub Actions ahead of June 2026 forced migration.
- `Stop()` only calls `enableXinput()` when `disabledXinputIDs` is non-empty.
- `New()` no longer calls `enableXinput()` unconditionally.
- `parseXinputIDs` is now a standalone testable function separate from the
  `xinput` subprocess call.

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
