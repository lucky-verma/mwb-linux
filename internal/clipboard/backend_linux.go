//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const waylandSocketPrefix = "wayland-"

// clipboardBackend isolates desktop clipboard access from the MWB protocol.
// The manager owns polling, hashing, and network transport; a backend only
// reads and writes the local desktop selection.
type clipboardBackend interface {
	name() string
	readText() string
	writeText(string) error
	readImage() []byte
	writeImage([]byte, string) error
	readFiles() []string
	writeFile(string) error
}

type clipboardCommands interface {
	output(string, ...string) ([]byte, error)
	input([]byte, string, ...string) error
}

type execClipboardCommands struct {
	env []string
}

func (c *execClipboardCommands) output(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = c.env
	return cmd.Output()
}

func (c *execClipboardCommands) input(data []byte, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = c.env
	cmd.Stdin = bytes.NewReader(data)
	// xclip and wl-copy leave a selection owner behind. Do not attach output
	// pipes here because a child can inherit them and keep Run waiting for EOF.
	return cmd.Run()
}

type x11Clipboard struct {
	commands clipboardCommands
	desktop  string
}

func (b *x11Clipboard) name() string { return "x11" }

func (b *x11Clipboard) readText() string {
	for _, command := range []struct {
		name string
		args []string
	}{
		{"xclip", []string{"-selection", "clipboard", "-o"}},
		{"xsel", []string{"--clipboard", "--output"}},
	} {
		out, err := b.commands.output(command.name, command.args...)
		if err == nil {
			return string(out)
		}
	}
	return ""
}

func (b *x11Clipboard) writeText(text string) error {
	var lastErr error
	for _, command := range []struct {
		name string
		args []string
	}{
		{"xclip", []string{"-selection", "clipboard"}},
		{"xsel", []string{"--clipboard", "--input"}},
	} {
		if err := b.commands.input([]byte(text), command.name, command.args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("xclip and xsel failed: %w", lastErr)
}

func (b *x11Clipboard) readImage() []byte {
	types, err := b.commands.output("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	if err != nil || !hasClipboardTarget(string(types), "image/png") {
		return nil
	}
	data, err := b.commands.output("xclip", "-selection", "clipboard", "-t", "image/png", "-o")
	if err != nil || len(data) == 0 {
		return nil
	}
	return data
}

func (b *x11Clipboard) writeImage(data []byte, mimeType string) error {
	return b.commands.input(data, "xclip", "-selection", "clipboard", "-t", mimeType, "-i")
}

func (b *x11Clipboard) readFiles() []string {
	types, err := b.commands.output("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o")
	if err != nil {
		return nil
	}
	target := clipboardFileTarget(string(types))
	if target == "" {
		return nil
	}
	payload, err := b.commands.output("xclip", "-selection", "clipboard", "-t", target, "-o")
	if err != nil {
		return nil
	}
	return parseFileURIs(string(payload))
}

func (b *x11Clipboard) writeFile(path string) error {
	target := preferredFileClipboardTarget(b.desktop)
	payload := fileClipboardPayload(path, target)
	return b.commands.input([]byte(payload), "xclip", "-selection", "clipboard", "-t", target, "-i")
}

type waylandClipboard struct {
	commands     clipboardCommands
	copyCommand  string
	pasteCommand string
	desktop      string
	fallback     *x11Clipboard
}

func (b *waylandClipboard) name() string { return "wayland" }

func (b *waylandClipboard) types() (string, error) {
	out, err := b.commands.output(b.pasteCommand, "--list-types")
	return string(out), err
}

func (b *waylandClipboard) readText() string {
	types, err := b.types()
	if err != nil {
		return b.fallback.readText()
	}
	target := clipboardTextTarget(types)
	if target == "" {
		return ""
	}
	out, err := b.commands.output(b.pasteCommand, "--no-newline", "--type", target)
	if err != nil {
		return b.fallback.readText()
	}
	return string(out)
}

func (b *waylandClipboard) writeText(text string) error {
	err := b.commands.input([]byte(text), b.copyCommand, "--type", "text/plain;charset=utf-8")
	if err == nil {
		return nil
	}
	return b.fallback.writeText(text)
}

func (b *waylandClipboard) readImage() []byte {
	types, err := b.types()
	if err != nil {
		return b.fallback.readImage()
	}
	target := clipboardImageTarget(types)
	if target == "" {
		return nil
	}
	data, err := b.commands.output(b.pasteCommand, "--type", target)
	if err != nil || len(data) == 0 {
		return b.fallback.readImage()
	}
	return data
}

func (b *waylandClipboard) writeImage(data []byte, mimeType string) error {
	err := b.commands.input(data, b.copyCommand, "--type", mimeType)
	if err == nil {
		return nil
	}
	return b.fallback.writeImage(data, mimeType)
}

func (b *waylandClipboard) readFiles() []string {
	types, err := b.types()
	if err != nil {
		return b.fallback.readFiles()
	}
	target := clipboardFileTarget(types)
	if target == "" {
		return nil
	}
	payload, err := b.commands.output(b.pasteCommand, "--type", target)
	if err != nil {
		return b.fallback.readFiles()
	}
	return parseFileURIs(string(payload))
}

func (b *waylandClipboard) writeFile(path string) error {
	target := preferredFileClipboardTarget(b.desktop)
	payload := fileClipboardPayload(path, target)
	err := b.commands.input([]byte(payload), b.copyCommand, "--type", target)
	if err == nil {
		return nil
	}
	return b.fallback.writeFile(path)
}

func clipboardTextTarget(types string) string {
	for _, target := range []string{
		"text/plain;charset=utf-8",
		"text/plain;charset=UTF-8",
		"text/plain",
		"UTF8_STRING",
		"STRING",
	} {
		if hasClipboardTarget(types, target) {
			return target
		}
	}
	return ""
}

func clipboardImageTarget(types string) string {
	for _, target := range []string{"image/png", "image/bmp"} {
		if hasClipboardTarget(types, target) {
			return target
		}
	}
	return ""
}

func hasClipboardTarget(types, target string) bool {
	for _, offered := range strings.Fields(types) {
		if offered == target {
			return true
		}
	}
	return false
}

func newClipboardBackend(display string) clipboardBackend {
	return newClipboardBackendWith(display, os.Getenv, os.Environ, exec.LookPath)
}

func newClipboardBackendWith(
	display string,
	getenv func(string) string,
	environ func() []string,
	lookPath func(string) (string, error),
) clipboardBackend {
	desktop := getenv("XDG_CURRENT_DESKTOP")
	baseEnv := environ()
	x11Env := replaceEnvironment(baseEnv, "DISPLAY", display)
	x11 := &x11Clipboard{
		commands: &execClipboardCommands{env: x11Env},
		desktop:  desktop,
	}

	runtimeDir := getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		candidate := fmt.Sprintf("/run/user/%d", os.Getuid())
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			runtimeDir = candidate
		}
	}
	explicitDisplay := getenv("WAYLAND_DISPLAY")
	waylandDisplay := detectWaylandDisplay(explicitDisplay, runtimeDir)
	if waylandDisplay == "" {
		return x11
	}

	copyCommand, copyErr := lookPath("wl-copy")
	pasteCommand, pasteErr := lookPath("wl-paste")
	if copyErr != nil || pasteErr != nil {
		slog.Warn("native Wayland clipboard unavailable, falling back to X11",
			"missing_wl_copy", copyErr != nil,
			"missing_wl_paste", pasteErr != nil)
		return x11
	}

	waylandEnv := replaceEnvironment(baseEnv, "WAYLAND_DISPLAY", waylandDisplay)
	if runtimeDir != "" {
		waylandEnv = replaceEnvironment(waylandEnv, "XDG_RUNTIME_DIR", runtimeDir)
	}
	if explicitDisplay == "" {
		slog.Info("Wayland display auto-detected", "display", waylandDisplay)
	}
	return &waylandClipboard{
		commands:     &execClipboardCommands{env: waylandEnv},
		copyCommand:  copyCommand,
		pasteCommand: pasteCommand,
		desktop:      desktop,
		fallback:     x11,
	}
}

// detectWaylandDisplay prefers the compositor-provided environment and falls
// back to a live socket. The socket path matters for user services because
// some compositors do not import WAYLAND_DISPLAY into systemd's environment.
func detectWaylandDisplay(explicit, runtimeDir string) string {
	if explicit != "" && waylandDisplayAlive(explicit, runtimeDir) {
		return explicit
	}
	if runtimeDir == "" {
		return ""
	}
	entries, err := os.ReadDir(runtimeDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), waylandSocketPrefix) || strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		if waylandDisplayAlive(entry.Name(), runtimeDir) {
			return entry.Name()
		}
	}
	return ""
}

func waylandDisplayAlive(display, runtimeDir string) bool {
	if display == "" {
		return false
	}
	path := display
	if !filepath.IsAbs(path) {
		if runtimeDir == "" {
			return false
		}
		path = filepath.Join(runtimeDir, display)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}
	conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func replaceEnvironment(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}
