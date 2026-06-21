# Debian Readiness

This document tracks what remains before `mwb-linux` is a credible candidate
for Debian upload and later Ubuntu sync.

## Current status

- Upstream release exists: `v0.4.0`.
- License is MIT.
- Go module has one binary and two external Go dependencies.
- The module now builds on Go 1.22 with Debian/Ubuntu-packaged dependency
  versions:
  - `github.com/BurntSushi/toml v1.3.2`
  - `golang.org/x/crypto v0.19.0`
- `debian/` packaging is present but deliberately marked `UNRELEASED`.
- The package name is `mwb-linux`; binary installed in `$PATH` is `mwb`.
- Current readiness: binary package builds cleanly enough for local review;
  source upload is blocked until the Go 1.22 compatibility change is either in
  a new upstream release or represented as a Debian patch.

## Recommended next move

Use an upstream-first path:

1. Land the Go 1.22 compatibility and Debian packaging changes in a PR.
2. Cut a small upstream release, likely `v0.4.1`, after CI passes.
3. Rebuild Debian packaging from the `v0.4.1` orig tarball.
4. Run lintian against source, binary, and changes artifacts in Debian unstable.
5. File the ITP only after the source package is clean.

This keeps the Debian package simple. The alternative is to keep `v0.4.0` as
the orig tarball and carry the Go 1.22 compatibility change as a Debian quilt
patch, but that is noisier for a first upload.

## Packaging choices

- Source format: `3.0 (quilt)`.
- Build helper: `dh-golang`.
- Runtime package installs:
  - `/usr/bin/mwb`
  - systemd user service
  - udev rules for `/dev/uinput` and `mwb-mouse` libinput flat profile
  - man page, README, architecture docs, and example config
- Autopkgtest currently performs a superficial CLI smoke test.
- Debian source package local artifacts are ignored via `debian/source/options`
  so `dist/`, `graphify-out/`, and local assistant state do not enter source
  diffs.

## Verification ledger

2026-06-21 on an Ubuntu Noble x86_64 test host:

- `go test ./...` passed with `/usr/lib/go-1.22/bin/go`.
- `dpkg-checkbuilddeps` passed against Ubuntu-packaged build dependencies.
- `dpkg-buildpackage -us -uc -b` produced
  `mwb-linux_0.4.0-1_amd64.deb`.
- The Debian build ran package tests through `dh_auto_test`.
- The built binary is PIE and dynamically linked with generated `libc6`
  dependency.
- Package contents include `/usr/bin/mwb`, user systemd unit, udev rules,
  README/architecture docs, example config, copyright, changelog, and man page.
- Extracted-package smoke check passed: `mwb -h` prints the expected CLI flags.
- `uscan --no-download --verbose` successfully detected GitHub tag `v0.4.0`.

Known verification limits:

- Ubuntu Noble lintian warns `unknown-field Static-Built-Using`; current Debian
  lintian documents this as the expected Go/Rust static provenance field. Treat
  this as an Ubuntu-lintian-version warning until checked in Debian unstable.
- Lintian warns `initial-upload-closes-no-bugs` until a real ITP bug exists.
- `dpkg-buildpackage -S` is not yet a clean upload artifact for `0.4.0-1`
  because the Go 1.22 compatibility change modifies upstream `go.mod`/`go.sum`
  relative to the existing `v0.4.0` orig tarball. Resolve this by either
  releasing a new upstream tag that includes the compatibility change or by
  carrying the change as a Debian quilt patch.

## Acceptance blockers to close before upload

- Commit and review the packaging branch in the upstream repo.
- Release a new upstream tag that contains the Go 1.22 compatibility change,
  or convert that change into a Debian quilt patch.
- File an ITP bug and add its real `Closes: #nnnnnn` entry to
  `debian/changelog`.
- Decide whether to maintain under the Debian Go Packaging Team on Salsa.
- Build in a Debian unstable clean environment with Debian-packaged Go
  dependencies only; do not rely on network module downloads.
- Run `lintian` on source, changes, and binary outputs; fix all errors and
  justify any remaining warnings.
- Produce a clean source package after choosing the upstream release vs. quilt
  patch route for the Go 1.22 compatibility change.
- Review trademark-safe wording around Microsoft PowerToys Mouse Without
  Borders compatibility.
- Verify source provenance for `docs/assets/banner.png` and `docs/assets/logo.png`
  or exclude nonessential generated artwork from the upstream tarball if a
  sponsor objects.
- Add a stronger autopkgtest if practical. Full bidirectional behavior needs a
  graphical/input-device environment, so the first Debian upload may reasonably
  keep the smoke test superficial.

## Sponsor path

1. Publish the packaging branch and make it build reproducibly.
2. Create a clean source package from a matching upstream release tarball.
3. Open an ITP bug against `wnpp`.
4. Add the real ITP close line and move `debian/changelog` from `UNRELEASED`
   to `unstable`.
5. Upload the source package to mentors.debian.net.
6. File an RFS bug against `sponsorship-requests`.
7. Respond to sponsor review, especially around naming, udev rules, service
   behavior, and testability.
