# Contributing to MWB Linux

Thank you for your interest in contributing! Here's how to get started.

## Development Setup

```bash
# Clone the repo
git clone https://github.com/lucky-verma/mwb-linux.git
cd mwb-linux

# Install Go 1.25+ (see go.mod for the exact version)
# https://go.dev/dl/

# Install dev dependencies
sudo apt install xdotool xclip

# Build
make build

# Run tests
make test

# Run linter (install golangci-lint first)
make lint

# Run all checks before committing
make check

# Optional native Wayland capture (requires libei-dev)
make test-wayland
```

## Submitting Changes

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-feature`
3. Make your changes
4. Run `make check` to ensure everything passes
5. Commit with a descriptive message
6. Push and open a Pull Request — the PR template will guide you through the
   required checklist (build, race detector, lint, and manual input-isolation
   safety checks)

> **Before touching `internal/capture/`**: read the **Critical Invariants**
> section in [docs/architecture.md](docs/architecture.md). Those rules protect
> input recovery, shutdown, and cursor ownership.

## Code Style

- Follow standard Go conventions (`gofmt`)
- Use `slog` for structured logging
- Add debug-level logs for protocol details, info-level for user-visible events
- Keep changes focused and add tests for behavior changes

## Architecture

Start with the [documentation index](docs/README.md). The
[architecture guide](docs/architecture.md) covers protocol and runtime details.

## Areas for Contribution

- **Wayland support** — Add native capture and clipboard backends while keeping
  X11/XWayland behavior stable
- **Virtual cursor correction** — Wire `UpdateRemoteScreen()` to incoming
  absolute coordinates to fix drift
- **Multi-monitor** — Proper screen geometry for `xrandr` multi-output setups
- **GUI** — System tray app with GTK
- **Auto-discovery** — Find MWB servers on the network
- **Packaging** — Flatpak, Snap, AUR packages

## Reporting Issues

Please include:

- Your Linux distribution and version
- Go version (`go version`)
- PowerToys version on Windows
- Debug logs (`mwb -debug`)
- Steps to reproduce
