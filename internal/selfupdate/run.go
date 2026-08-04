package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// probeTimeout bounds the local helper commands used to describe the install:
// neither dpkg-query nor systemctl should ever hang an update.
const probeTimeout = 5 * time.Second

// unitName is the systemd user unit shipped by the installers.
const unitName = "mwb.service"

// Options controls one `mwb update` invocation.
type Options struct {
	CurrentVersion string
	CheckOnly      bool
	Force          bool // reinstall even when already up to date
	Restart        bool // restart mwb.service after a successful install
	Out            io.Writer
}

// Run performs the check-and-install flow and explains what it did.
func Run(ctx context.Context, opts Options) error {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	// Progress output goes to a plain writer; a failed write here is not worth
	// aborting an update over, so it is swallowed in one place.
	say := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }

	rel, err := LatestRelease(ctx, Repo)
	if err != nil {
		return fmt.Errorf("check for updates: %w", err)
	}

	say("installed: %s\nlatest:    %s\n", displayVersion(opts.CurrentVersion), rel.Version)

	upgradable := IsNewer(opts.CurrentVersion, rel.Version)
	if opts.CheckOnly {
		if upgradable {
			say("\nAn update is available. Run `mwb update` to install it.\n")
		} else {
			say("\nAlready up to date.\n")
		}
		return nil
	}
	if err := validateInstallVersion(opts.CurrentVersion, opts.Force); err != nil {
		return err
	}
	if !upgradable && !opts.Force {
		say("\nAlready up to date.\n")
		return nil
	}

	// An apt-installed binary belongs to dpkg. Overwriting it behind the package
	// manager's back leaves the two disagreeing, and the next apt operation would
	// silently revert the update.
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate running binary: %w", err)
	}
	if exePath, err = filepath.EvalSymlinks(exePath); err != nil {
		return fmt.Errorf("resolve running binary: %w", err)
	}
	if pkg, managed := packageOwner(ctx, exePath); managed {
		// No APT repository publishes this project, so `apt install --only-upgrade`
		// would report the package is already the newest version. Point at the
		// release artifact, which is the only path that actually upgrades a
		// dpkg-managed install.
		say("\n%s is managed by the %q package, so it is not replaced here.\n", exePath, pkg)
		say("Download the %s .deb and install it with dpkg, which keeps dpkg in sync:\n\n", rel.Version)
		say("    https://github.com/%s/releases/latest\n", Repo)
		say("    sudo dpkg -i %s_*.deb\n", pkg)
		return nil
	}

	asset := AssetName()
	assetURL, ok := rel.Assets[asset]
	if !ok {
		return fmt.Errorf("release %s has no asset %q (available: %s)",
			rel.Version, asset, strings.Join(assetNames(rel), ", "))
	}
	sumsURL, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s publishes no checksums.txt; refusing to install an unverified binary", rel.Version)
	}

	// Fail on an unwritable destination before spending a download on it.
	info, err := os.Stat(exePath)
	if err != nil {
		return fmt.Errorf("stat running binary: %w", err)
	}
	if err := checkWritable(exePath); err != nil {
		return err
	}

	say("\nDownloading checksums…\n")
	sumsBody, err := get(ctx, sumsURL)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}
	sums, err := ParseChecksums(sumsBody)
	sumsBody.Close() //nolint:errcheck
	if err != nil {
		return fmt.Errorf("parse checksums: %w", err)
	}
	want, ok := sums[asset]
	if !ok {
		return fmt.Errorf("checksums.txt has no entry for %q; refusing to install an unverified binary", asset)
	}

	say("Downloading %s (%s)…\n", asset, rel.Version)
	data, err := download(ctx, assetURL, want)
	if err != nil {
		return err
	}
	say("Checksum verified (%s…).\n", want[:16])

	backup, err := backupBinary(exePath, opts.CurrentVersion)
	if err != nil {
		return err
	}
	say("Backed up current binary to %s\n", backup)

	if err := replaceBinary(exePath, data, info.Mode().Perm()); err != nil {
		return err
	}
	say("\nInstalled %s to %s\n", rel.Version, exePath)

	// The old inode keeps running until the service is restarted.
	if unit, running := runningUnit(ctx); running && opts.Restart {
		say("Restarting %s…\n", unit)
		if err := restartUnit(ctx, unit); err != nil {
			return fmt.Errorf("installed %s, but restart failed: %w", rel.Version, err)
		}
		say("Restarted %s.\n", unit)
	} else if running {
		say("\nThe running service is still on the previous version. Restart it with:\n\n")
		say("    systemctl --user restart %s\n", unit)
	} else {
		say("\nRestart any running mwb process to pick up the new version.\n")
	}
	return nil
}

// validateInstallVersion prevents a source build from being silently replaced
// by the newest release. A source build has no comparable semantic version, so
// replacement must be an explicit --force choice.
func validateInstallVersion(current string, force bool) error {
	if _, ok := ParseVersion(current); !ok && !force {
		return fmt.Errorf("refusing to replace %s without --force", displayVersion(current))
	}
	return nil
}

const maxBackupAttempts = 100

// backupBinary preserves the exact inode about to be replaced. Hard-linking in
// the same directory is atomic, consumes no duplicate file data, and keeps the
// rollback usable after replaceBinary renames a new inode over dest.
func backupBinary(dest, currentVersion string) (string, error) {
	label := "dev"
	if parts, ok := ParseVersion(currentVersion); ok {
		label = fmt.Sprintf("%d.%d.%d", parts[0], parts[1], parts[2])
	}
	base := fmt.Sprintf("%s-%s.bak", dest, label)
	for i := 0; i < maxBackupAttempts; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d", base, i)
		}
		if err := os.Link(dest, candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("back up current binary: %w", err)
		}
	}
	return "", fmt.Errorf("back up current binary: %d backup names already exist", maxBackupAttempts)
}

func displayVersion(v string) string {
	if v == "" || v == "dev" {
		return "dev (built from source)"
	}
	return v
}

func assetNames(rel *Release) []string {
	names := make([]string, 0, len(rel.Assets))
	for n := range rel.Assets {
		names = append(names, n)
	}
	return names
}

// checkWritable reports whether the binary can be replaced, with a message that
// tells the user what to do rather than surfacing a bare EACCES.
func checkWritable(exePath string) error {
	dir := filepath.Dir(exePath)
	probe, err := os.CreateTemp(dir, ".mwb-perm-*")
	if err == nil {
		name := probe.Name()
		probe.Close()   //nolint:errcheck
		os.Remove(name) //nolint:errcheck
		return nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot write to %s: re-run with sudo (sudo mwb update)", dir)
	}
	return fmt.Errorf("cannot write to %s: %w", dir, err)
}

// packageOwner reports the dpkg package owning path, if any.
func packageOwner(ctx context.Context, path string) (string, bool) {
	dpkg, err := exec.LookPath("dpkg-query")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, dpkg, "-S", path).Output()
	if err != nil {
		return "", false // not owned by any package
	}
	name, _, found := strings.Cut(string(out), ":")
	if !found || strings.TrimSpace(name) == "" {
		return "", false
	}
	return strings.TrimSpace(name), true
}

// runningUnit reports the systemd user unit if one is active.
func runningUnit(ctx context.Context) (string, bool) {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, systemctl, "--user", "is-active", unitName).Output()
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(string(out)) != "active" {
		return "", false
	}
	return unitName, true
}

func restartUnit(ctx context.Context, unit string) error {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return fmt.Errorf("find systemctl: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, systemctl, "--user", "restart", unit).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message != "" {
			return fmt.Errorf("systemctl: %s: %w", message, err)
		}
		return fmt.Errorf("systemctl: %w", err)
	}
	return nil
}
