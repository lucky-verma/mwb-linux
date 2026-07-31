package filetransfer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnsafeName means the peer's filename could not be reduced to something
// safe to create inside the destination directory.
var ErrUnsafeName = errors.New("unsafe file name")

// SafeName reduces a peer-supplied name to a plain basename that cannot escape
// the destination directory.
//
// The name arrives from a remote machine, so it is treated as hostile. MWB
// itself only ever uses Path.GetFileName on it; this does the same and then
// rejects the residue that a bare basename call still lets through.
//
// Both separators are stripped, not just the platform's: a Windows peer sends
// backslash-separated paths, which filepath on Linux does not treat as a
// separator at all, so "..\\..\\etc\\passwd" would survive as a single
// "filename" and then be created literally.
func SafeName(raw string) (string, error) {
	name := raw

	// Windows peers send backslash paths; normalise before taking a basename.
	name = strings.ReplaceAll(name, "\\", "/")

	// Drop any drive letter such as "C:" that a Windows peer may prefix.
	if len(name) >= 2 && name[1] == ':' {
		name = name[2:]
	}

	name = filepath.Base(filepath.Clean("/" + name))

	switch name {
	case "", ".", "..", "/":
		return "", fmt.Errorf("%w: %q reduces to nothing usable", ErrUnsafeName, raw)
	}
	if strings.ContainsRune(name, os.PathSeparator) {
		return "", fmt.Errorf("%w: %q still contains a separator", ErrUnsafeName, raw)
	}
	// NUL cannot appear in a path and would be silently truncated by the kernel.
	if strings.ContainsRune(name, '\x00') {
		return "", fmt.Errorf("%w: %q contains NUL", ErrUnsafeName, raw)
	}
	// A leading dot would hide the file from the user who is expecting it.
	if strings.HasPrefix(name, ".") {
		name = "_" + name
	}
	return name, nil
}

// maxCollisionAttempts bounds the " (n)" search so a directory full of
// collisions cannot spin forever.
const maxCollisionAttempts = 1000

// DestPath returns a path inside dir that does not yet exist, creating dir if
// needed. Existing files are never overwritten; " (1)", " (2)" and so on are
// appended before the extension, matching what browsers do.
//
// The returned path is verified to still be inside dir after symlink
// resolution, so a pre-planted symlink in the destination cannot redirect the
// write elsewhere.
func DestPath(dir, safeName string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create destination directory: %w", err)
	}

	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("resolve destination directory: %w", err)
	}

	ext := filepath.Ext(safeName)
	stem := strings.TrimSuffix(safeName, ext)

	for i := 0; i < maxCollisionAttempts; i++ {
		candidate := safeName
		if i > 0 {
			candidate = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		full := filepath.Join(realDir, candidate)

		// Join already cleans the path, but assert the invariant rather than
		// trusting it: nothing may be created outside realDir.
		if filepath.Dir(full) != realDir {
			return "", fmt.Errorf("%w: %q would land outside %s", ErrUnsafeName, safeName, realDir)
		}

		// Lstat, not Stat: a dangling symlink must count as occupied, otherwise
		// the create below would follow it and write through to its target.
		if _, err := os.Lstat(full); err == nil {
			continue // taken
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("check destination: %w", err)
		}
		return full, nil
	}
	return "", fmt.Errorf("%w: %d name collisions in %s", ErrUnsafeName, maxCollisionAttempts, realDir)
}
