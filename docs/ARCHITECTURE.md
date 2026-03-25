# MWB Bidirectional Architecture

## Overview

This is a Linux client for Microsoft PowerToys Mouse Without Borders (MWB) that enables **bidirectional** keyboard and mouse sharing between a Linux PC and a Windows laptop over the local network. It is the first implementation of MWB host-mode from a non-Windows client.

## How It Works

```
  Ubuntu PC (Linux)                         Work Laptop (Windows)
  ┌─────────────────┐                      ┌─────────────────────┐
  │  Mouse    │──evdev──┐            │  PowerToys MWB      │
  │  Keyboard     │──evdev──┤            │  (Server/Host)      │
  │                 │         │            │                     │
  │  mwb client     │◄────────┘            │  Receiver.cs        │
  │  ┌───────────┐  │    TCP/AES-256-CBC   │  ┌───────────────┐  │
  │  │ capture   │──┼──── Mouse/KB pkts ──►│  │ SendInput()   │  │
  │  │ handler   │◄─┼──── Mouse/KB pkts ──◄│  │ InputHook     │  │
  │  │ protocol  │  │    Port 15101        │  │ MachineStuff  │  │
  │  └───────────┘  │                      │  └───────────────┘  │
  │                 │                      │                     │
  │  uinput virtual │                      │  Touchpad/Keyboard  │
  │  mouse+keyboard │                      │  (physical)         │
  └─────────────────┘                      └─────────────────────┘
```

## Connection Lifecycle

### 1. TCP Connect + Encryption Setup

```
Linux                          Windows
  │                               │
  ├──TCP dial :15101──────────────►│
  │                               │
  ├──16 random bytes (IV seed)───►│  AES-256-CBC streams established
  │◄──16 random bytes (IV seed)───┤  using PBKDF2-SHA512 derived key
  │                               │
```

**Key derivation**: `PBKDF2(SHA512, securityKey, UTF16LE("18446744073709551615"), 50000 iterations) → 32-byte AES key`

**Fixed IV**: ASCII bytes of `"1844674407370955"` (first 16 chars of uint64.MaxValue)

### 2. Handshake

```
Linux                          Windows
  │                               │
  ├──10x Handshake(126)──────────►│  Contains random 128-bit challenge
  │◄──10x Handshake(126)─────────┤  Contains server's random challenge
  │                               │
  ├──HandshakeAck(127)───────────►│  Machine1-4 = ~server.Machine1-4
  │◄──HandshakeAck(127)──────────┤  Machine1-4 = ~our.Machine1-4
  │                               │
  ├──HeartbeatEx(51, Des=255)────►│  Triggers AddToMachinePool()
  │                               │
```

### 3. Steady State — Heartbeats & Matrix

```
Linux                          Windows
  │                               │
  │◄──Matrix|Hi(130)──────────────┤  Server asks for machine layout info
  ├──Hello(3)─────────────────────►│  We confirm our presence
  │                               │
  │◄──HeartbeatEx(51)─────────────┤  Keep-alive
  ├──HeartbeatEx(51)──────────────►│  Echo back with our machine name
  │                               │
```

## Cursor Switching Protocol

### Direction: Windows → Linux (Touchpad)

```
1. User moves touchpad to right edge of Windows screen
2. Server detects edge via MoveToMyNeighbourIfNeeded()
3. Server calls PrepareToSwitchToMachine(linuxID)
4. Server sends MachineSwitched(77) to Linux
5. Server starts sending Mouse(123) packets with relative coords (±100000)
6. Linux handler injects mouse via uinput virtual device
7. Linux cursor moves on screen
```

### Direction: Linux → Windows (Mouse)

```
1. mwb polls cursor position via xdotool every 50ms
2. Cursor hits left edge (x=0)
3. mwb disables local device in X11 via xinput (Ubuntu stops receiving input)
4. mwb sends burst of absolute Mouse(123) packets to server center (32767,32767)
5. Server's Receiver.cs processes Mouse with Des==MachineID:
   - Forces desMachineID = self (self-reclaim)
   - Calls InputSimulation.SendMouse() → Win32 SendInput
   - Cursor appears on Windows
6. mwb forwards local device evdev events as absolute Mouse packets (0-65535 coords)
7. Virtual cursor (remoteX/remoteY) tracks position on remote
```

### Direction: Linux → Windows → Back to Linux (Return)

```
1. Virtual cursor remoteX reaches remoteWidth (right edge of remote)
2. mwb re-enables local device in X11 via xinput
3. mwb moves local cursor to Ubuntu center via xdotool
4. mwb sets active=true, clears switchSent
5. local mouse now controls Ubuntu again
```

## Packet Wire Format

### Standard Packet (32 bytes)

```
Offset  Size  Field
──────  ────  ─────
0       1     Type (PackageType enum)
1       1     Checksum (sum of bytes 2-31)
2-3     2     Magic number (24-bit hash of security key)
4-7     4     Packet ID (int32, little-endian, must be non-zero)
8-11    4     Src machine ID (uint32)
12-15   4     Des machine ID (uint32, or 255=broadcast)
16-31   16    Payload union (Mouse/Keyboard/Handshake data)
```

### Extended Packet (64 bytes)

Same as above, plus:
```
32-63   32    Machine name (ASCII, space-padded)
```

### Mouse Payload (bytes 16-31)

```
16-19   4     X (int32) — absolute 0-65535, or relative ±100000+delta
20-23   4     Y (int32) — absolute 0-65535, or relative ±100000+delta
24-27   4     WheelDelta (int32) — 120 = one notch
28-31   4     DwFlags (int32) — WM_MOUSEMOVE=0x200, WM_LBUTTONDOWN=0x201, etc.
```

### Keyboard Payload (bytes 16-31)

```
16-23   8     DateTime (int64) — usually 0
24-27   4     WVk (int32) — Windows Virtual Key code
28-31   4     DwFlags (int32) — 0=keydown, 0x80=keyup, 0x01=extended
```

## Key Packet Types

| Type | Value | Direction | Purpose |
|------|-------|-----------|---------|
| Hi | 2 | Server→Client | Device discovery ping |
| Hello | 3 | Client→Server | Discovery response (includes machine name) |
| ByeBye | 4 | Either | Disconnect notification |
| Heartbeat | 20 | Either | Keep-alive |
| HeartbeatEx | 51 | Either | Extended keep-alive (with machine name) |
| HideMouse | 50 | Server→Client | Tell old remote to hide cursor |
| MachineSwitched | 77 | Server→Client | Cursor now on your machine |
| NextMachine | 121 | Client→Server | Request cursor switch to another machine |
| Keyboard | 122 | Either | Keyboard input event |
| Mouse | 123 | Either | Mouse input event |
| Handshake | 126 | Either | Challenge during connection setup |
| HandshakeAck | 127 | Either | Challenge response (bitwise NOT) |
| Matrix\|* | 128+ | Server→Client | Machine layout information |

## Critical Implementation Details

### 1. Packet ID Must Be Non-Zero
The server has a zero-initialized dedup ring buffer. ID=0 packets are silently dropped.

### 2. HandshakeAck Src Must Be Our MachineID
If Src=0, server stores ID.NONE and never routes packets to our socket.

### 3. HeartbeatEx After Handshake
Must send HeartbeatEx with Des=255 (broadcast) to trigger AddToMachinePool() on the server.

### 4. Matrix Packet Handling
Server sends Matrix|Hi (type 130 = 128|2) packets. We must respond with Hello to be registered in the machine layout. Without this, the server's edge detection doesn't know we exist.

### 5. Absolute vs Relative Mouse
- **Absolute** (0-65535): Server calls `InputSimulation.SendMouse()`, no edge checking
- **Relative** (±100000 sentinel): Server calls `MoveMouseRelative()`, then checks edges via `MoveToMyNeighbourIfNeeded()`

With "Move mouse relatively" **OFF** on the server, absolute mode avoids bounce-back issues.

### 6. xinput for Device Isolation
When controlling the remote, `xinput disable` prevents the local device from moving the Ubuntu cursor. `xinput enable` restores it when returning. This is more reliable than EVIOCGRAB which had issues with device restoration.

### 7. Timing and Debouncing
- **Edge detection cooldown**: 2 seconds between switches
- **Arrival cooldown**: 3 seconds after cursor arrives before checking edges
- **Switch grace**: 500ms evdev suppression after sending switch packets

## Package Structure

```
cmd/mwb/main.go              Entry point, flag parsing, connection loop
internal/
  config/config.go            TOML config loading (~/.config/mwb/config.toml)
  protocol/
    types.go                  Packet type constants, message flags
    packet.go                 Packet struct, Marshal/Unmarshal
    crypto.go                 AES key derivation, magic number, stamp/validate
    stream.go                 EncryptWriter, DecryptReader (AES-256-CBC)
  network/
    client.go                 TCP connection, IV exchange, handshake, Send/Recv
    receiver.go               Main receive loop, heartbeat/matrix handling
    handler.go                Mouse/Keyboard injection, MachineSwitched/NextMachine callbacks
  input/
    uinput.go                 Virtual mouse/keyboard via /dev/uinput
    keymap_linux.go           Windows VK → Linux evdev mapping
    reverse_keymap_linux.go   Linux evdev → Windows VK mapping
    buttons.go                BTN_LEFT/RIGHT/MIDDLE constants
  capture/
    capture_linux.go          Edge detection, evdev monitoring, xinput grab, remote cursor tracking
    screen_linux.go           Screen resolution via xrandr
```

## Configuration

```toml
# ~/.config/mwb/config.toml
host = "192.168.1.164"        # Windows machine IP
key = "vE8_hB2;dN9>rK9<"     # Must match PowerToys MWB security key
name = "thinkersloop"          # Machine name (max 15 chars)
port = 15100                   # Base port (message port = 15101)
```

## Running

```bash
# Basic (receive only — Windows controls Linux)
mwb

# Bidirectional (Linux can also control Windows)
sudo mwb -bidi -edge left

# With debug logging
sudo mwb -bidi -edge left -debug
```

## Requirements

### Windows Side
- PowerToys with Mouse Without Borders enabled
- "Move mouse relatively" set to **OFF**
- Security key generated and shared

### Linux Side
- `/dev/uinput` accessible (user in `input` group)
- `xdotool` installed (for cursor position polling)
- `xinput` installed (for device isolation)
- `xrandr` installed (for screen detection)
- Run with `sudo` for evdev access, or configure udev rules

## Future Work

- [ ] Clipboard sharing (text copy/paste between machines)
- [ ] File drag-and-drop
- [ ] Multi-monitor support
- [ ] Wayland native support (replace xdotool/xinput)
- [ ] Auto-detect remote screen dimensions
- [ ] Systemd service with proper permissions
- [ ] Smoother cursor transition animations
