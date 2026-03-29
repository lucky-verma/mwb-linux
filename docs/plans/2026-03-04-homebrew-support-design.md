# Homebrew Support Design

Date: 2026-03-04
Issue: bketelsen/mwb#1

## Summary

Add Homebrew tap support for `mwb` on Linux and macOS using goreleaser's built-in `brews` feature. On release, goreleaser generates and pushes a Homebrew formula to `bketelsen/homebrew-tap`.

## Approach

Use goreleaser's `brews` stanza (Option A). GoReleaser Pro already handles formula generation, checksum injection, and pushing to a tap repo. The existing split/merge CI pipeline is preserved; the formula push happens during `goreleaser continue --merge` on the ubuntu-latest runner.

## Changes

### `.goreleaser.yml`

Add a `brews` section:

```yaml
brews:
  - name: mwb
    ids:
      - mwb-linux
      - mwb-darwin
    repository:
      owner: bketelsen
      name: homebrew-tap
      token: "{{ .Env.HOMEBREW_TAP_TOKEN }}"
    homepage: https://github.com/lucky-verma/mwb-linux
    description: Mouse Without Borders client for Linux and macOS
    license: MIT
    install: |
      bin.install "mwb"
    service: |
      run opt_bin/"mwb"
      keep_alive true
      log_path var/"log/mwb.log"
      error_log_path var/"log/mwb.log"
    caveats: |
      On macOS, grant Accessibility permissions before starting:
        System Settings > Privacy & Security > Accessibility > Add mwb

      On Linux, set up uinput access:
        sudo modprobe uinput
        echo 'KERNEL=="uinput", GROUP="input", MODE="0660"' | \
          sudo tee /etc/udev/rules.d/99-uinput.rules
        sudo udevadm control --reload-rules && sudo udevadm trigger
        sudo usermod -aG input $USER
```

The `service` block generates a `brew services`-compatible plist (macOS) or systemd unit (Linux), replacing manual LaunchAgent/systemd setup for Homebrew-installed users.

### `.github/workflows/release.yml`

Add `HOMEBREW_TAP_TOKEN` to the `merge` job's env block:

```yaml
env:
  GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  GORELEASER_KEY: ${{ secrets.GORELEASER_KEY }}
  HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

### `homebrew-tap` repo

Initialize the empty `bketelsen/homebrew-tap` repo with a `Formula/` directory. Goreleaser creates/updates `Formula/mwb.rb` on each release.

### `README.md`

Add a Homebrew installation section:

```markdown
## Installation via Homebrew

\`\`\`bash
brew tap bketelsen/tap
brew install mwb
brew services start mwb  # optional: start on login
\`\`\`
```

## Prerequisites (manual setup)

1. Create a GitHub PAT with `repo` scope scoped to `bketelsen/homebrew-tap`
2. Add it as `HOMEBREW_TAP_TOKEN` secret in `bketelsen/mwb` repo settings

## User Experience

```bash
brew tap bketelsen/tap
brew install mwb
brew services start mwb
```

On macOS, `brew services start` installs a LaunchAgent in `~/Library/LaunchAgents/`. On Linux, it manages a systemd user service.

## Non-Goals

- No changes to deb/rpm packaging
- No changes to the split/merge CI structure
- No handwritten formula template
