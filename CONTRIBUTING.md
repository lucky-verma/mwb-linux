# Contributing to MWB Linux

Thank you for your interest in contributing! Here's how to get started.

## Development Setup

```bash
# Clone the repo
git clone https://github.com/lucky-verma/mwb-linux.git
cd mwb-linux

# Install Go 1.24+
# https://go.dev/dl/

# Install dev dependencies
sudo apt install xdotool xinput xclip

# Build
make build

# Run tests
make test

# Run linter (install golangci-lint first)
make lint

# Run all checks before committing
make check
```

## Submitting Changes

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-feature`
3. Make your changes
4. Run `make check` to ensure everything passes
5. Commit with a descriptive message
6. Push and open a Pull Request

## Code Style

- Follow standard Go conventions (`gofmt`)
- Use `slog` for structured logging
- Add debug-level logs for protocol details, info-level for user-visible events
- Keep functions focused and under 50 lines where possible

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed protocol documentation.

## Areas for Contribution

- **Wayland support** — Replace xdotool/xinput with libei/portals
- **Multi-monitor** — Proper screen geometry handling
- **GUI** — System tray app with GTK
- **Auto-discovery** — Find MWB servers on the network
- **Packaging** — Flatpak, Snap, AUR packages
- **Testing** — Unit tests for protocol, encryption, packet handling

## Reporting Issues

Please include:
- Your Linux distribution and version
- Go version (`go version`)
- PowerToys version on Windows
- Debug logs (`mwb -debug`)
- Steps to reproduce
