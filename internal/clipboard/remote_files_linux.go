//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
)

const stagePrefix = "item-"

// HandleFileChannel receives one PowerToys clipboard-channel payload.
//
// Real files are written to private cache storage and exposed to the desktop as
// a text/uri-list clipboard selection. They are deliberately not written to a
// visible Downloads directory: the user's file manager creates the destination
// file only when the user presses Ctrl+V. Oversized text and images remain
// in-memory and use their normal clipboard handlers.
func (m *Manager) HandleFileChannel(r io.Reader, maxSize int64) (*filetransfer.Result, error) {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()

	stageDir, err := m.newStageDir()
	if err != nil {
		return nil, err
	}
	keepStage := false
	defer func() {
		if !keepStage {
			removeStage(stageDir)
		}
	}()

	res, err := filetransfer.Receive(r, stageDir, maxSize)
	if err != nil {
		return nil, err
	}
	if res.Path == "" {
		m.HandleFileChannelPayload(res.Name, res.Inline)
		return res, nil
	}

	if err := m.setFileClipboard(res.Path); err != nil {
		return nil, fmt.Errorf("publish received file to clipboard: %w", err)
	}

	m.mu.Lock()
	m.lastHash = "file:" + res.Path
	m.justSet = time.Now()
	m.mu.Unlock()

	keepStage = true
	m.removeOtherStages(stageDir)
	slog.Info("remote file staged on Linux clipboard",
		"name", res.Name, "bytes", res.Size)
	return res, nil
}

func defaultStageRoot() string {
	cache, err := os.UserCacheDir()
	if err == nil && cache != "" {
		return filepath.Join(cache, "mwb", "clipboard")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("mwb-%d", os.Getuid()), "clipboard")
}

func (m *Manager) newStageDir() (string, error) {
	if err := os.MkdirAll(m.stageRoot, 0o700); err != nil {
		return "", fmt.Errorf("create clipboard staging root: %w", err)
	}
	// Tighten a root created by an older build or an unusual umask.
	if err := os.Chmod(m.stageRoot, 0o700); err != nil {
		return "", fmt.Errorf("secure clipboard staging root: %w", err)
	}
	dir, err := os.MkdirTemp(m.stageRoot, stagePrefix)
	if err != nil {
		return "", fmt.Errorf("create clipboard staging directory: %w", err)
	}
	return dir, nil
}

// removeOtherStages keeps only the clipboard selection that was most recently
// published. Cleanup happens after the new selection succeeds, so a failed
// transfer never breaks the previously usable clipboard item.
func (m *Manager) removeOtherStages(keep string) {
	entries, err := os.ReadDir(m.stageRoot)
	if err != nil {
		slog.Warn("could not inspect clipboard staging root", "err", err)
		return
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), stagePrefix) {
			continue
		}
		path := filepath.Join(m.stageRoot, entry.Name())
		if path != keep {
			removeStage(path)
		}
	}
}

func removeStage(path string) {
	if err := os.RemoveAll(path); err != nil {
		slog.Warn("could not remove clipboard staging directory", "path", path, "err", err)
	}
}

// setLocalFileClipboard makes Linux file managers see the staged file as a
// copied file. xclip remains the X11 selection owner after its parent exits;
// native Wayland desktops receive the same selection through XWayland.
func (m *Manager) setLocalFileClipboard(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	target := preferredFileClipboardTarget(os.Getenv("XDG_CURRENT_DESKTOP"))
	payload := fileClipboardPayload(abs, target)

	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", target, "-i")
	cmd.Env = append(os.Environ(), "DISPLAY="+m.display)
	cmd.Stdin = bytes.NewBufferString(payload)
	// Do not use Output/CombinedOutput here. xclip forks a selection-owner
	// process which keeps inherited output pipes open; waiting for pipe EOF
	// would turn a successful clipboard set into a timeout.
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("xclip: %w", err)
	}
	return nil
}

func preferredFileClipboardTarget(desktop string) string {
	desktop = strings.ToLower(desktop)
	for _, family := range []string{"gnome", "unity", "cinnamon", "mate"} {
		if strings.Contains(desktop, family) {
			return gnomeFileTarget
		}
	}
	return uriListTarget
}

func fileClipboardPayload(path, target string) string {
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	if target == gnomeFileTarget {
		// Nautilus rejects a trailing newline because it becomes an empty URI
		// entry. Its own serializer emits exactly "copy\n<uri>".
		return "copy\n" + uri
	}
	return uri + "\r\n"
}
