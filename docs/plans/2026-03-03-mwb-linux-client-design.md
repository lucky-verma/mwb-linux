# MWB Linux Client - Design Document

## Summary

A Go application that implements the Microsoft PowerToys Mouse Without Borders (MWB) wire protocol, enabling a Linux machine to act as a client controlled from a Windows MWB host. Input injection uses uinput for universal Wayland (and X11) compatibility.

## Scope

**In scope (MVP):**
- Client-only mode (Linux is controlled by a Windows MWB host)
- Mouse movement and click injection via uinput
- Keyboard input injection via uinput with VK-to-evdev keycode mapping
- AES-256-CBC encrypted communication matching MWB's exact protocol
- Heartbeat responses to maintain connection
- Automatic reconnection with exponential backoff
- TOML configuration file

**Out of scope (future):**
- Host mode (controlling other machines from Linux)
- Clipboard sync (text, images)
- File drag-and-drop
- Multi-monitor awareness
- Screen capture
- GUI / system tray

## Architecture

```
┌──────────────────────────────────────────────────┐
│                   mwb (Go binary)                │
│                                                  │
│  ┌────────────┐  ┌────────────┐  ┌────────────┐ │
│  │   Network   │  │  Protocol   │  │   Input    │ │
│  │   Layer     │──│  Layer      │──│   Layer    │ │
│  │            │  │            │  │            │ │
│  │ TCP client │  │ Packet     │  │ uinput     │ │
│  │ AES-256    │  │ decode     │  │ virtual    │ │
│  │ CBC stream │  │ dispatch   │  │ mouse/kbd  │ │
│  └────────────┘  └────────────┘  └────────────┘ │
│                                                  │
│  ┌────────────┐  ┌────────────┐                  │
│  │   Config    │  │  Keymap     │                  │
│  │   (TOML)    │  │  VK→evdev   │                  │
│  └────────────┘  └────────────┘                  │
└──────────────────────────────────────────────────┘
         │
         │ TCP :15100
         ▼
┌──────────────────┐
│  Windows MWB     │
│  Host            │
└──────────────────┘
```

Three core layers plus two support modules.

## Protocol Details

### Transport

- TCP port 15100 (configurable) for mouse/keyboard/control messages
- Port 15101 reserved for clipboard/file transfer (future)

### Connection Lifecycle

1. TCP connect to Windows host on port 15100
2. IV exchange: both sides send 16-byte random block to seed CBC chain
3. Wrap socket in AES-256-CBC encrypt/decrypt streams
4. Send Handshake packet with machine ID and name
5. Receive HandshakeAck confirming registration in machine pool
6. Enter main receive loop: dispatch packets by type, respond to heartbeats

### Encryption

| Parameter       | Value                                       |
|-----------------|---------------------------------------------|
| Algorithm       | AES-256-CBC                                 |
| Key size        | 256 bits (32 bytes)                         |
| Block size      | 128 bits (16 bytes)                         |
| Padding         | Zeros                                       |
| Key derivation  | PBKDF2-SHA512, 50,000 iterations            |
| Salt            | UTF-8 bytes of "18446744073709551615"       |

Magic number: 24-bit hash of the key placed in packet bytes 2-3 for quick validation before decryption.

### Packet Format

Fixed-size packets: 32 bytes (standard) or 64 bytes (extended).

**Header (bytes 0-15):**

| Offset | Size | Field | Type     |
|--------|------|-------|----------|
| 0      | 4    | Type  | uint32   |
| 4      | 4    | Id    | int32    |
| 8      | 4    | Src   | uint32   |
| 12     | 4    | Des   | uint32   |

**Mouse payload (Type=123, bytes 12-31):**

| Offset | Size | Field      |
|--------|------|------------|
| 12     | 4    | X          |
| 16     | 4    | Y          |
| 20     | 4    | WheelDelta |
| 24     | 4    | dwFlags    |

Coordinates are absolute, normalized to 0-65535 range.

**Keyboard payload (Type=122, bytes 16-31):**

| Offset | Size | Field    |
|--------|------|----------|
| 16     | 8    | DateTime |
| 24     | 4    | wVk      |
| 28     | 4    | dwFlags  |

### Key Packet Types (MVP)

| Type         | Value | Action                          |
|--------------|-------|---------------------------------|
| Handshake    | 126   | Send on connect                 |
| HandshakeAck | 127   | Confirm connection              |
| Mouse        | 123   | Inject mouse event              |
| Keyboard     | 122   | Inject keyboard event           |
| Heartbeat    | 20    | Respond to keep connection alive |
| ByeBye       | 4     | Host disconnecting              |

## Input Injection

### uinput Virtual Devices

Two virtual devices created on startup:

1. **Virtual Mouse** - `EV_ABS` (ABS_X, ABS_Y range 0-65535), `EV_KEY` (BTN_LEFT, BTN_RIGHT, BTN_MIDDLE), `EV_REL` (REL_WHEEL)
2. **Virtual Keyboard** - `EV_KEY` for all standard keycodes

### Coordinate Mapping

MWB absolute coordinates (0-65535) are injected directly as `ABS_X`/`ABS_Y` events. The uinput device is configured with absolute axis range 0-65535, matching MWB's normalization. The compositor handles mapping to screen pixels.

### Keycode Mapping

Static table mapping ~160 Windows VK codes to Linux evdev `KEY_*` codes. Sourced from Barrier/Input Leap's well-tested mapping tables.

### Permissions

User must be in the `input` group, or:
- Run with `CAP_DAC_OVERRIDE` capability
- Configure a udev rule for `/dev/uinput` access

## Configuration

File: `~/.config/mwb/config.toml`

```toml
host = "192.168.1.100"    # Windows MWB host IP
key = "YourSecurityKey"   # MWB security key (min 16 chars)
name = "linux-desktop"    # This machine's name (max 15 chars)
port = 15100              # TCP port (default 15100)
```

## Error Handling

- Connection drops: exponential backoff reconnect (1s, 2s, 4s, ... max 30s)
- Decryption failures: log and drop packet, don't disconnect
- Heartbeat timeout: trigger reconnect
- uinput creation failure: fatal error with clear permission instructions

## Logging

`slog` structured logging to stderr. Levels: debug (packet-level), info (connection events), error (failures).

## Project Structure

```
mwb/
├── cmd/mwb/main.go          # Entry point, config loading, signal handling
├── internal/
│   ├── protocol/
│   │   ├── packet.go         # Packet structs, binary serialization
│   │   ├── types.go          # PackageType enum, constants
│   │   └── crypto.go         # AES-256-CBC, PBKDF2, magic number
│   ├── network/
│   │   ├── client.go         # TCP connection, encrypted stream, handshake
│   │   └── receiver.go       # Packet receive loop, dispatch
│   ├── input/
│   │   ├── uinput.go         # Virtual device creation, event injection
│   │   └── keymap.go         # VK code → evdev keycode mapping
│   └── config/
│       └── config.go         # TOML config loading
├── go.mod
├── go.sum
└── docs/plans/
```

## Dependencies

- `github.com/BurntSushi/toml` - config parsing
- `golang.org/x/crypto` - PBKDF2-SHA512
- Standard library: `crypto/aes`, `crypto/cipher`, `net`, `encoding/binary`, `log/slog`, `os/signal`
- uinput via direct ioctl syscalls (no CGo)

## References

- [PowerToys MWB source](https://github.com/microsoft/PowerToys/tree/main/src/modules/MouseWithoutBorders) (MIT license)
- [Wireshark dissector](https://gist.github.com/mikeclayton/4c36d7ba0ac67d7263781766b62ed8d1) by Mike Clayton
- Key source files: `PackageType.cs`, `DATA.cs`, `SocketStuff.cs`, `Encryption.cs`, `Receiver.cs`
