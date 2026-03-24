# Homebrew Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add goreleaser-managed Homebrew tap support so users can install `mwb` on Linux and macOS via `brew install bketelsen/tap/mwb`.

**Architecture:** Add a `brews` stanza to `.goreleaser.yml` that generates and pushes a Homebrew formula to `bketelsen/homebrew-tap` during the release merge step. The formula uses goreleaser's `service` block for `brew services` integration, and includes platform-specific caveats.

**Tech Stack:** GoReleaser Pro v2, Homebrew, GitHub Actions, `bketelsen/homebrew-tap` (exists, empty)

---

### Task 1: Initialize the homebrew-tap repository

**Files:**
- No files in this repo to modify — this task operates on the remote `bketelsen/homebrew-tap` repo

**Step 1: Create the Formula directory in the tap repo**

```bash
cd /tmp
git clone https://github.com/bketelsen/homebrew-tap.git
cd homebrew-tap
mkdir -p Formula
touch Formula/.gitkeep
git add Formula/.gitkeep
git commit -m "feat: initialize tap with Formula directory"
git push origin main
cd /tmp/symphony_workspaces/bketelsen_mwb_1
```

Expected: `Formula/.gitkeep` pushed to `bketelsen/homebrew-tap` main branch.

**Step 2: Verify**

```bash
gh api repos/bketelsen/homebrew-tap/contents/Formula --jq '.[].name'
```

Expected: `.gitkeep`

---

### Task 2: Add brews section to .goreleaser.yml

**Files:**
- Modify: `.goreleaser.yml`

**Step 1: Append the brews section**

Open `.goreleaser.yml` and add the following after the `nfpms` block and before `checksum`:

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
    homepage: https://github.com/bketelsen/mwb
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

**Step 2: Validate goreleaser config**

```bash
goreleaser check --config .goreleaser.yml 2>&1 || echo "goreleaser not installed locally, skip"
```

If goreleaser is available locally, expected: `config is valid`. If not installed, skip — CI will validate.

**Step 3: Commit**

```bash
git add .goreleaser.yml
git commit -m "feat: add homebrew tap support to goreleaser config"
```

---

### Task 3: Update release workflow to pass HOMEBREW_TAP_TOKEN

**Files:**
- Modify: `.github/workflows/release.yml`

**Step 1: Add HOMEBREW_TAP_TOKEN to the merge job env**

Find the `merge` job's `Run GoReleaser Merge` step in `.github/workflows/release.yml`. Add `HOMEBREW_TAP_TOKEN` to its `env` block:

```yaml
      - name: Run GoReleaser Merge
        uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser-pro
          version: "~> v2"
          args: continue --merge
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
          GORELEASER_KEY: ${{ secrets.GORELEASER_KEY }}
          HOMEBREW_TAP_TOKEN: ${{ secrets.HOMEBREW_TAP_TOKEN }}
```

**Step 2: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: pass HOMEBREW_TAP_TOKEN to goreleaser merge job"
```

---

### Task 4: Update README with Homebrew installation instructions

**Files:**
- Modify: `README.md`

**Step 1: Read the current README**

Open `README.md` and find the installation section (or the top of the file).

**Step 2: Add Homebrew installation section**

Add the following section after any existing installation content (or near the top under a "## Installation" heading):

```markdown
## Installation

### Homebrew (Linux and macOS)

```bash
brew tap bketelsen/tap
brew install mwb
```

To start `mwb` automatically on login:

```bash
brew services start mwb
```

On macOS you must grant Accessibility permissions after installing:
> System Settings → Privacy & Security → Accessibility → Add `mwb`

### Linux (deb/rpm)

Download the latest `.deb` or `.rpm` from the [releases page](https://github.com/bketelsen/mwb/releases).
```

**Step 3: Commit**

```bash
git add README.md
git commit -m "docs: add homebrew installation instructions"
```

---

### Task 5: Document the required GitHub secret

**Files:**
- No code changes — this is a human checklist item documented here

**Manual prerequisite (repository owner must do this):**

1. Go to https://github.com/settings/tokens and create a classic PAT with `repo` scope (or a fine-grained PAT scoped to `bketelsen/homebrew-tap` with read/write Contents permission)
2. Go to https://github.com/bketelsen/mwb/settings/secrets/actions
3. Click "New repository secret"
4. Name: `HOMEBREW_TAP_TOKEN`
5. Value: the PAT created in step 1

**Verification (after secret is set):** The next release tag push will trigger CI. Check the merge job logs — goreleaser should output a line like:
```
• pushing formula        formula=Formula/mwb.rb repo=bketelsen/homebrew-tap
```

---

### Task 6: Create PR

**Step 1: Push branch and open PR**

```bash
git push origin symphony/bketelsen_mwb_1
gh pr create \
  --title "feat: add homebrew tap support" \
  --body "Adds goreleaser brews section to publish mwb to bketelsen/homebrew-tap on release.

## Changes
- \`.goreleaser.yml\`: adds \`brews\` section targeting bketelsen/homebrew-tap
- \`.github/workflows/release.yml\`: passes HOMEBREW_TAP_TOKEN to merge job
- \`README.md\`: adds Homebrew installation instructions

## Manual prerequisite
A \`HOMEBREW_TAP_TOKEN\` PAT secret must be added to this repo's secrets before a release triggers. See docs/plans/2026-03-04-homebrew-support.md Task 5 for instructions.

Closes #1"
```

---

## Notes

- The `service` block in the `brews` stanza uses Homebrew's built-in service management. On macOS it installs a LaunchAgent at `~/Library/LaunchAgents/homebrew.mwb.plist`; on Linux it installs a systemd user unit. This is cleaner than the manual `com.mwb.agent.plist` for Homebrew-installed users.
- The existing `packaging/com.mwb.agent.plist` remains for non-Homebrew macOS users.
- The existing deb/rpm packaging via `nfpms` is unchanged.
- `HOMEBREW_TAP_TOKEN` is only needed in the `merge` job because the formula push happens during `goreleaser continue --merge`, not during the per-platform `--split` runs.
