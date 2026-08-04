# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Native Wayland clipboard access through `wl-copy` and `wl-paste`, selected
  automatically when a Wayland session is active. Text, PNG images, and
  single-file selections keep the existing MWB network behavior.
- An experimental source build for native Wayland bidirectional input using
  the XDG InputCapture portal and libei. It is isolated behind the
  `wayland_portal` build tag while KDE, GNOME, and Hyprland testing is pending.

### Changed
- Clipboard access now has separate Wayland and X11 backends. X11 keeps its
  existing `xclip` and `xsel` path, and remains the fallback if native Wayland
  commands fail.
- The installer and Linux packages include `wl-clipboard`.
- Normal builds keep the existing X11 capture path. Portal-enabled builds use
  capability detection in `auto` mode and never mix libei events with raw evdev
  capture.

## [0.6.2] - 2026-08-03

### Added
- `mwb update --restart` restarts an active user service after a successful
  install.

### Changed
- Cursor edge polling now uses one persistent X11 connection instead of
  spawning `xdotool getmouselocation` every 10 ms. `xdotool` remains only for
  the infrequent cursor recenter operation.
- `mwb update` creates a versioned hard-link backup beside the running binary
  before replacement and refuses to replace an unversioned source build unless
  `--force` is explicit.
- Public documentation is reduced to current architecture and file-transfer
  guides; stale implementation plans and unused media are removed.

### Fixed
- Known PowerToys zero-byte file-transfer error headers are rejected before any
  staged file or clipboard entry is created. Legitimate empty files
  remain supported, and rejected transfers cannot be copied back to Windows as
  fake files.

## [0.6.1] - 2026-08-01

### Fixed
- **File copy uses the clipboard port.** MWB runs two listeners —
  `skMessageServer` on `TcpPort + 1` and `skClipboardServer` on `TcpPort` — and
  only the second routes a connection into `ShakeHand` and the file receiver.
  0.6.0 assumed a single port for both, so outbound copies were written to the
  control server, which answered with a control `Handshake` and never read
  them, and inbound copies were never offered at all because MWB dials the
  clipboard port that nothing was listening on. Both ports are open on a
  Windows peer, so the dial always succeeded and every failure was silent.
  A live PowerToys peer now accepts the corrected channel before bytes are sent.
- **Both halves of the file channel handshake are exchanged.** `ShakeHand`
  writes its own 64-byte header and reads the peer's, in both roles. This side
  did neither, so a pushing Windows client blocked in `ReadEx` until its 30s
  receive timeout and then reported the channel as rejected. These headers are
  raw `DATA` structs, not normal control packets: stamping one changes the
  32-bit package type and makes PowerToys reject it. Outbound sends now require
  a valid raw reply, so a rejected channel can no longer be logged as a
  successful file send.
- `UnmarshalPacket` could not decode the machine name on a file channel header:
  it skipped bytes 32-63 whenever `ClipboardData` was set, which is always for a
  clipboard type. MWB matches that name in
  `ResolveID(name) == package.Src` before accepting the channel.
- **Linux to Windows file copy now runs.** 0.6.0 shipped every part of the
  sending half — `text/uri-list` clipboard detection, `DialFile`, the
  block-aligned writer — but nothing ever assigned `Manager.OnFileCopy`, so the
  callback stayed nil and the branch was unreachable. Copying a file on Linux
  sent its name as text instead, which is the exact behaviour that detection
  exists to prevent. Files are staged on the Windows clipboard so they can be
  pasted into any chosen File Explorer folder.
- **Windows to Linux now follows PowerToys' deferred clipboard flow.** PowerToys
  does not push a file when it is copied; it broadcasts `Clipboard`, remembers
  it for 30 seconds, and retrieves it only when the cursor switches onto the
  receiving machine. Linux now does the same before sending `ClipboardAsk`.
  The received file backs a native `x-special/gnome-copied-files` or generic
  `text/uri-list` selection in private `$XDG_CACHE_HOME/mwb/clipboard` storage,
  so copying on Windows does not spam a visible Linux folder; the chosen
  destination is created only on Ctrl+V. Only the newest staged selection is
  retained. The reply header also reuses the active control connection's
  machine ID, which PowerToys requires when it checks
  `ResolveID(name) == Src`.
- Linux activation can arrive as either `MachineSwitched` or `NextMachine`,
  depending on connection direction and screen topology. Both now trigger a
  pending Windows clipboard pull. The bidirectional capturer's local
  virtual-edge return is a third activation path with no inbound packet; it now
  triggers the same callback. Reconnecting also baselines the existing Linux
  selection instead of transmitting that stale file back to Windows.

### Added
- Outbound copies are validated before the connection is opened. Copying a
  folder is common, and it no longer costs the peer a file channel that it only
  ever sees opened and abandoned.
- A 60s idle write deadline bounds an outbound transfer whose peer stops
  reading. Total elapsed time is deliberately not capped, because a large file
  over a slow link is legitimate; what is capped is time without progress. The
  sending goroutine is tracked by the clipboard manager's WaitGroup and the
  reconnect loop waits on that, so a wedged peer would otherwise stall
  reconnection for as long as TCP took to give up.

## [0.6.0] - 2026-07-31

### Added
- **File copy between machines, both directions.** Copy a file on Windows and it
  lands in `~/Downloads/mwb`; copy one on Linux and it goes to the peer. File
  bytes do not travel over the control packet stream: MWB opens a second
  connection on the base/clipboard port, and packet types 70/71/72/75 only
  coordinate the Windows drag-and-drop UI. Matches MWB's own scope, so single
  files only, no folders, with a 100 MB default cap taken from
  `MAX_CLIPBOARD_FILE_SIZE_CAN_BE_SENT`. Configurable via `file_transfer`,
  `file_dir` and `max_file_size`. The wire format and the receive-side safety
  controls are documented in `docs/file-transfer.md`.
- `mwb update` downloads and installs the latest GitHub release, verifying it
  against the SHA-256 in that release's `checksums.txt` and replacing the binary
  atomically. `--check` reports availability without installing; `--force`
  reinstalls. Refuses to overwrite a dpkg-managed install, and explains the
  `sudo` requirement instead of failing with a bare permission error.
- `mwb version` prints the installed version. The release pipeline already
  passed `-X main.version`, but no such variable existed, so the flag was
  silently doing nothing and every build was unversioned.

### Changed
- **Device isolation now uses `EVIOCGRAB` instead of `xinput disable`.** The
  grab is owned by the file descriptor, so the kernel restores local input when
  mwb's descriptors close — including on crash, `SIGKILL` and OOM. Previously,
  suppression was global X11 state that outlived the process: if `xinput enable`
  never ran (the remote never handed the cursor back, or mwb was killed), the
  machine was left with no mouse and no keyboard, and nothing would restore
  them. Recovering meant replugging hardware, which could crash Xorg and drop
  the whole desktop session.
- Devices are now classified by capability, mirroring udev's `input_id`, instead
  of matching `razer|wooting` in the device name. Laptop touchpads, touchscreens
  and graphics tablets are now isolated correctly; power and sleep buttons, lid
  switches and audio-jack detection are deliberately left alone.
- Isolation is now a single ioctl per device rather than one `xinput`
  subprocess per device, which removes the ~1-2s compositor stall on return that
  was previously documented as a known limitation.
- `xinput` is no longer a runtime dependency.

### Fixed
- Inbound connections from the configured peer were rejected whenever it
  connected from an address other than the one in `host`. A dual-stack Windows
  peer opens the connection from an IPv6 link-local address, which no lookup of
  its IPv4 can ever return, so every attempt was refused and the link flapped
  (5 reconnects in 20 minutes on a normal session). The allowlist now refuses
  globally routable sources — the internet still cannot reach the handshake —
  and permits local-network sources to attempt it. The shared key remains the
  authentication control; this widens who may attempt authentication, never who
  passes it.
- Isolation direction is now derived from cursor ownership inside a dedicated
  mutex instead of being stated by each caller. Outbound switches run on
  `pollCursorEdge` and returns run on the network handler, so a release
  belonging to a finished switch-back could land after the grab for the next
  switch-out and leave local input live while the cursor was on the remote
  machine — a window of dual-cursor movement, observed live as
  `grabbed … count=11 of=12` immediately followed by `released … count=12`.
  Devices already in the target state are also skipped, since re-grabbing a
  device this process already holds returns `EBUSY` and undercounted the result.
- `Stop()` could hang forever in `wg.Wait()`. Input devices were opened blocking,
  so their fds were not registered with Go's poller and `Close()` did not
  interrupt a parked `read(2)`; a monitor goroutine for a silent device (power
  button, audio-jack detect) never returned. Devices are now opened `O_NONBLOCK`.
- The tracked device set was captured once at startup, so devices that appeared
  later — wireless receivers re-enumerating on wake, a mouse plugged in
  mid-session — were never isolated and leaked motion to the local display.
  `grabInput()` now refreshes the set first, and devices that disappear are
  dropped.
- mwb's own `mwb-mouse` / `mwb-keyboard` uinput devices are never grabbed.
  Grabbing them would swallow the events mwb injects, silently breaking remote
  typing and clicking. Virtual devices belonging to other tools are skipped for
  the same reason.

## [0.5.1] - 2026-07-02

### Changed
- Updated CI checkout to `actions/checkout@v7`.
- Updated `github.com/BurntSushi/toml` from 1.3.2 to 1.6.0.

### Fixed
- German inbound AltGr chords now drop Windows' synthetic Ctrl event before
  injecting right Alt, fixing level-3 keys such as `@`, `~`, and `|` (#23).
- Release `.deb` packages now ship `packaging/mwb.service`, whose
  `/usr/local/bin/mwb` `ExecStart` matches the packaged binary path.
- Installed systemd user services now default to receive-only mode (`mwb`)
  instead of bidirectional mode, preventing surprise Linux → Windows mouse
  handoff on startup.
- Installers now remove the legacy root `mwb-linux.service` if present, because
  it can keep port 15101 bound and force bidirectional mode after an upgrade.
- **False return from remote far edge**: `MachineSwitched` and `NextMachine`
  packets are now accepted only when the requested return matches the Linux
  edge configured by `-edge`. This prevents a remote laptop on Ubuntu's left
  from handing control back to Ubuntu when the cursor touches the remote
  laptop's far-left edge because Windows has a rotated matrix or wrap-style
  behavior.

## [0.5.0] - 2026-06-29

### Added
- Inbound keyboard layout profiles via `keyboard_layout` in `config.toml`.
  `auto` detects the local Linux layout when possible; supported profiles cover
  common US, German, French, Belgian, Spanish/Latin American, Italian, UK,
  Portuguese, Nordic, Swiss, and Dutch layouts. This fixes layout-sensitive
  Windows-to-Linux key forwarding such as German `z/y`, `ß`, and umlaut keys
  (#23, reported by @5inf; German mapping verified by @hm2dev). The German
  profile is end-to-end verified; the other profiles are derived from standard
  layout geometry and covered by unit tests.

### Fixed
- **`make install` ran a stale binary (#23)**: the per-user systemd unit shipped
  `ExecStart=/usr/local/bin/mwb`, but `make install` writes the binary to
  `~/go/bin/mwb`. The service therefore started an old `/usr/local/bin/mwb` (or
  failed) instead of the freshly built binary, which hid the keyboard-layout fix
  when run under systemd. The unit now uses `ExecStart=%h/go/bin/mwb`, and the
  README documents `make install` as a no-sudo per-user install. Found by
  @hm2dev.

## [0.4.1] - 2026-06-21

### Added
- Debian packaging metadata, man page, example config, basic CLI autopkgtest,
  and Debian readiness documentation.
- Public PR checklist items for keeping package metadata and release text free
  of unintended local metadata.

### Changed
- Lowered the Go module baseline to Go 1.22 and dependency versions available
  in the Debian/Ubuntu packaging path.
- Updated release package maintainer metadata to use the public GitHub noreply
  address.

## [0.4.0] - 2026-06-21

### Fixed
- **Wobbly cursor on Wayland (#5)**: two-part fix.
  1. Virtual mouse now declares `INPUT_PROP_POINTER` via `UI_SET_PROPBIT`
     so libinput stops misclassifying the device. Regression test in
     `uinput_test.go` reads `/sys/class/input/inputN/properties` to verify
     the bit stays set.
  2. Ship `packaging/99-mwb-libinput.rules` udev rule that tags the
     `mwb-mouse` device with `LIBINPUT_ACCEL_PROFILE=flat` and
     `LIBINPUT_ACCEL_SPEED=0`. Reporter's `libinput debug-events` showed
     network-driven packet bursts produce variable input deltas (5–11×
     variance per axis); libinput's default adaptive accel curve was
     amplifying that variance into visible wobble. Flat profile maps
     deltas linearly so jitter is no longer multiplied. X11 path was
     unaffected throughout because XTest bypasses libinput.

### Added
- Configurable cursor speed: `accel_multiplier` in `config.toml` (default 2.0)
  scales raw evdev deltas when controlling Windows. The Windows side applies no
  acceleration of its own (absolute positioning), so this is the only speed
  knob — lower it (e.g. `1.0`, `0.5`) if the cursor feels too fast. Resolves #15.
- Inbound cursor speed: `inbound_multiplier` in `config.toml` (default 1.0)
  scales Windows-to-Linux cursor deltas. `1.0` preserves exact mirroring; raise
  it when inbound movement feels too slow.
- Clipboard sharing can now be disabled: `clipboard = false` in `config.toml`
  or the `-no-clipboard` flag (flag overrides config). Default stays enabled.
  When off, the client never starts the clipboard manager, so it won't override
  the Windows clipboard. Resolves #12.
- X-button support (back/forward): `BTN_SIDE`/`BTN_EXTRA` registered on virtual
  mouse, `WM_XBUTTONDOWN`/`WM_XBUTTONUP` handled in input handler.
- Horizontal scroll: `REL_HWHEEL` registered, `WM_MOUSEHWHEEL` handled —
  two-finger horizontal swipe from Windows trackpad now works on Ubuntu.
- 15 regression tests in `internal/capture/` covering xinput state management,
  mutex invariants, edge gate logic, and floating slave filtering.
- PR template (`.github/PULL_REQUEST_TEMPLATE.md`) with build, race, lint, and
  xinput safety checklists.
- `docs/architecture.md`: documented 5 critical invariants with code examples
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

### Documentation
- `docs/architecture.md`: replaced ASCII diagrams with Mermaid (component flow,
  connection lifecycle, cursor switching) matching the README theme. Corrected
  stale values to match the code: edge poll 10ms (was 50ms), switch grace 100ms
  (was 500ms), `canSwitch`/`canReturn` gates instead of the old 2s/3s cooldowns,
  and edge-based entry position (was "center"). Softened the "first
  implementation" claim.
- `README.md`: documented the new cursor-speed config options and corrected the
  cursor-speed/drift limitation note.
- `CONTRIBUTING.md`: Go version requirement bumped to 1.25+ to match `go.mod`.

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

[Unreleased]: https://github.com/lucky-verma/mwb-linux/compare/v0.6.2...HEAD
[0.6.2]: https://github.com/lucky-verma/mwb-linux/compare/v0.6.1...v0.6.2
[0.6.1]: https://github.com/lucky-verma/mwb-linux/compare/v0.6.0...v0.6.1
[0.6.0]: https://github.com/lucky-verma/mwb-linux/compare/v0.5.1...v0.6.0
[0.5.1]: https://github.com/lucky-verma/mwb-linux/compare/v0.5.0...v0.5.1
[0.5.0]: https://github.com/lucky-verma/mwb-linux/compare/v0.4.1...v0.5.0
[0.4.1]: https://github.com/lucky-verma/mwb-linux/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/lucky-verma/mwb-linux/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/lucky-verma/mwb-linux/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/lucky-verma/mwb-linux/compare/v0.1.0...v0.3.0
[0.1.0]: https://github.com/lucky-verma/mwb-linux/releases/tag/v0.1.0
