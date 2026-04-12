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
6. Push and open a Pull Request — the PR template will guide you through the
   required checklist (build, race detector, lint, and manual xinput safety checks)

> **Before touching `internal/capture/`**: read the **Critical Invariants**
> section in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Violations of the
> mutex, floating slave, and cursor position invariants have caused real
> production bugs that were hard to diagnose.

## Code Style

- Follow standard Go conventions (`gofmt`)
- Use `slog` for structured logging
- Add debug-level logs for protocol details, info-level for user-visible events
- Keep functions focused and under 50 lines where possible

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for detailed protocol documentation.

## Areas for Contribution

- **Wayland support** — Replace xdotool/xinput with compositor extensions (major rewrite of capture subsystem)
- **XInput2 events** — Replace 100 forks/sec xdotool polling with `XI_RawMotion` event subscription
- **EVIOCGRAB** — Replace xinput name-matching with kernel-level exclusive grab (works for all brands)
- **Virtual cursor correction** — Wire `UpdateRemoteScreen()` to incoming absolute coords to fix drift
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
