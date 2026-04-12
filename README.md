<p align="center">
  <img src="docs/assets/banner.png" alt="MWB Linux — Mouse Without Borders for Linux" width="800">
</p>

<p align="center">
  Share your keyboard, mouse, and clipboard seamlessly between Linux and Windows.
</p>

<p align="center">
  <a href="#features">Features</a> &bull;
  <a href="#installation">Installation</a> &bull;
  <a href="#quick-start">Quick Start</a> &bull;
  <a href="#how-it-works">How It Works</a> &bull;
  <a href="#configuration">Configuration</a> &bull;
  <a href="#contributing">Contributing</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/platform-Linux-blue" alt="Platform">
  <img src="https://img.shields.io/badge/language-Go-00ADD8" alt="Go">
  <img src="https://img.shields.io/badge/license-MIT-green" alt="License">
  <img src="https://img.shields.io/badge/protocol-MWB%20Compatible-orange" alt="MWB Compatible">
</p>

---

## What is this?

MWB Linux is a native Linux client that connects to **Microsoft PowerToys Mouse Without Borders** on Windows. Move your mouse to the edge of the screen, and it seamlessly jumps to the other machine — along with your keyboard and clipboard.

```mermaid
flowchart LR
    A["🐧 <b>Linux PC</b><br/>Mouse · Keyboard"] <-->|" 🖱️ Mouse · ⌨️ Keyboard · 📋 Clipboard "| B["🪟 <b>Windows PC</b><br/>Mouse · Keyboard"]
```

> Move your mouse to the screen edge — the cursor seamlessly jumps to the other machine.

No extra software needed on Windows — just PowerToys, which is already installed on millions of machines.

## Features

- **Bidirectional mouse sharing** — Control both machines from either keyboard/mouse
- **Seamless edge switching** — Move cursor to screen edge, it appears on the other machine
- **Clipboard sync** — Copy text or images on one machine, paste on the other
- **Keyboard forwarding** — Type on your Linux keyboard, text appears on Windows
- **Full mouse support** — Scroll wheel, horizontal scroll, and side buttons (back/forward)
- **Encrypted** — AES-256-CBC encryption with PBKDF2 key derivation
- **Device isolation** — When controlling Windows, your Linux cursor doesn't move
- **Dual-mode connection** — Acts as both server and client for fast reconnection
- **Zero config on Windows** — Works with existing PowerToys MWB setup
- **Lightweight** — Single binary, ~5MB, no GUI dependencies

## Demo

| Direction | What happens |
|-----------|-------------|
| Mouse hits left edge on Linux | Cursor appears on Windows, Linux input disabled |
| Mouse hits right edge on Windows | Cursor returns to Linux, input restored |
| Ctrl+C on Windows | Text/image available on Linux clipboard |
| Ctrl+C on Linux | Text/image available on Windows clipboard |
| Type on Linux keyboard | Text appears in focused Windows app |

## Installation

### One-Line Install (Ubuntu/Debian)

```bash
curl -fsSL https://raw.githubusercontent.com/lucky-verma/mwb-linux/main/scripts/install.sh | sudo bash
```

### From .deb Package

Download from [Releases](https://github.com/lucky-verma/mwb-linux/releases):

```bash
# Download latest release
wget https://github.com/lucky-verma/mwb-linux/releases/latest/download/mwb-linux_amd64.deb

# Install (automatically sets up udev rules and dependencies)
sudo dpkg -i mwb-linux_amd64.deb

# Add yourself to the input group
sudo usermod -aG input $USER
```

### From Binary

```bash
# Download binary
wget https://github.com/lucky-verma/mwb-linux/releases/latest/download/mwb-linux-amd64
chmod +x mwb-linux-amd64
sudo mv mwb-linux-amd64 /usr/local/bin/mwb

# Install dependencies
sudo apt install xdotool xinput xclip

# Setup permissions
sudo bash -c 'modprobe uinput && echo uinput > /etc/modules-load.d/uinput.conf'
echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | sudo tee /etc/udev/rules.d/99-mwb-uinput.rules
sudo udevadm control --reload-rules
sudo usermod -aG input $USER
```

### From Source

```bash
git clone https://github.com/lucky-verma/mwb-linux.git
cd mwb-linux
make build
sudo make install
```

> **Note:** Log out and back in after installation for group changes to take effect.

## Quick Start

### 1. Get the security key from Windows

Open **PowerToys** → **Mouse Without Borders** → copy the **Security Key**.

### 2. Configure

```bash
mkdir -p ~/.config/mwb
cat > ~/.config/mwb/config.toml << EOF
host = "192.168.1.100"        # Your Windows machine's IP
key = "YourSecurityKey"       # From PowerToys MWB
name = "linux"                # This machine's name (max 15 chars)
EOF
```

### 3. Run

```bash
# Receive only (Windows controls Linux)
mwb

# Bidirectional (Linux also controls Windows)
sudo mwb -bidi -edge left
```

### 4. Add your Linux machine on Windows

In PowerToys MWB, enter the security key and device name to connect.

## How It Works

MWB Linux implements the full Mouse Without Borders protocol:

1. **Dual-mode connection** — Listens on port 15101 AND connects outbound (first one wins)
2. **Handshake** — AES-256-CBC encrypted challenge-response with PBKDF2-SHA512 key derivation
3. **Heartbeats** — Proactive keepalive every 5s prevents Windows from dropping the connection
4. **Edge detection** — 10ms cursor polling detects screen edges, instant switching with bounce prevention
5. **Input forwarding** — Mouse (absolute coords) and keyboard (VK codes) sent as MWB packets
6. **Device isolation** — `xinput disable/enable` prevents dual cursor movement during remote control
7. **Clipboard** — Bidirectional text/image sync via compressed clipboard packets

For detailed protocol documentation, see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Configuration

### config.toml

| Field | Default | Description |
|-------|---------|-------------|
| `host` | (required) | Windows machine IP address |
| `key` | (required) | MWB security key (from PowerToys) |
| `name` | hostname | This machine's display name |
| `port` | 15100 | Base port (message port = 15101) |
| `remote_width` | 1920 | Remote screen width in pixels |
| `remote_height` | 1080 | Remote screen height in pixels |
| `edge` | left | Screen edge for switching: `left` or `right` |

### CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-bidi` | false | Enable bidirectional input (Linux → Windows) |
| `-edge` | *(from config)* | Override edge from config: `left` or `right` |
| `-debug` | false | Enable debug logging |
| `-config` | ~/.config/mwb/config.toml | Config file path |

### Windows Side Requirements

- **PowerToys** installed with Mouse Without Borders enabled
- **"Move mouse relatively"** set to **OFF** (required for bidirectional mode)
- **"Share clipboard"** set to **ON** (for clipboard sync)
- **"Block screen saver on other machines"** set to **ON** (recommended, keeps connection alive)
- Security key shared with Linux client
- Windows Firewall must allow ports **15100-15101** (TCP inbound/outbound)

## Troubleshooting

### "permission denied" on /dev/uinput
Run the setup permissions commands above, then log out and back in.

### Clipboard not syncing
Ensure `xclip` is installed: `sudo apt install xclip`

### Mouse controls both screens simultaneously
Run with `-bidi` flag and `sudo` for device isolation via xinput.

### Connection refused
- Check Windows firewall allows port 15100-15101
- Verify the IP address in config.toml
- Ensure PowerToys MWB is enabled on Windows

### Cursor bounces back immediately
Set "Move mouse relatively" to OFF in PowerToys MWB settings.

## Project Structure

```
cmd/mwb/              CLI entry point
internal/
  capture/            Edge detection, evdev capture, xinput device isolation
  clipboard/          Bidirectional clipboard sync (text + images)
  config/             TOML configuration
  input/              Virtual mouse/keyboard via uinput
  network/            TCP connection, encryption, packet send/receive
  protocol/           MWB packet types, serialization, AES-256-CBC
docs/
  ARCHITECTURE.md     Detailed protocol and architecture documentation
scripts/
  install.sh          Installation helper script
```

## Known Limitations

- **Keyboard on Windows lock screen** — Keyboard input may not work on the Windows lock screen (Winlogon desktop security restriction)
- **Middle mouse button auto-scroll** — Middle-click auto-scroll (scroll lock mode) does not work in browsers; normal middle-click works
- **First connection** — Initial handshake takes ~3-16s depending on Windows MWB state; subsequent reconnects are instant
- **X11 only** — Edge detection and device isolation use `xdotool`/`xinput` (Wayland requires compositor extensions and is a planned rewrite)
- **Virtual cursor drift** — Remote cursor tracking uses a fixed 2× acceleration; may drift from actual position over extended use. Set `accel_multiplier` in config if needed

## Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Building

```bash
make build    # Build binary
make test     # Run tests
make lint     # Run linter
make check    # All of the above
```

## Acknowledgments

- [Microsoft PowerToys](https://github.com/microsoft/PowerToys) — Mouse Without Borders is part of PowerToys (MIT License). This project implements the MWB network protocol for Linux.
- [bketelsen/mwb](https://github.com/bketelsen/mwb) — Initial Go implementation of the MWB receive-only client that this project builds upon.
- The MWB protocol specification was derived from the open-source PowerToys codebase.

## License

MIT License — see [LICENSE](LICENSE) for details.
