//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type clipboardInputCall struct {
	data []byte
	name string
	args []string
}

type fakeClipboardCommands struct {
	outputFunc func(string, ...string) ([]byte, error)
	inputFunc  func([]byte, string, ...string) error
	inputs     []clipboardInputCall
}

func (f *fakeClipboardCommands) output(name string, args ...string) ([]byte, error) {
	if f.outputFunc == nil {
		return nil, errors.New("unexpected output command")
	}
	return f.outputFunc(name, args...)
}

func (f *fakeClipboardCommands) input(data []byte, name string, args ...string) error {
	f.inputs = append(f.inputs, clipboardInputCall{
		data: append([]byte(nil), data...),
		name: name,
		args: append([]string(nil), args...),
	})
	if f.inputFunc == nil {
		return nil
	}
	return f.inputFunc(data, name, args...)
}

func TestNewClipboardBackendPrefersNativeWayland(t *testing.T) {
	runtimeDir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "wayland-2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	env := map[string]string{
		"WAYLAND_DISPLAY":     "wayland-2",
		"XDG_RUNTIME_DIR":     runtimeDir,
		"XDG_CURRENT_DESKTOP": "Hyprland",
	}
	getenv := func(key string) string { return env[key] }
	lookPath := func(name string) (string, error) { return "/usr/bin/" + name, nil }

	backend := newClipboardBackendWith(":9", getenv, func() []string {
		return []string{"PATH=/usr/bin", "DISPLAY=:old"}
	}, lookPath)

	wayland, ok := backend.(*waylandClipboard)
	if !ok {
		t.Fatalf("backend = %T, want native Wayland", backend)
	}
	if wayland.copyCommand != "/usr/bin/wl-copy" || wayland.pasteCommand != "/usr/bin/wl-paste" {
		t.Fatalf("Wayland commands = %q/%q", wayland.copyCommand, wayland.pasteCommand)
	}
	commands := wayland.commands.(*execClipboardCommands)
	if got := environmentValue(commands.env, "WAYLAND_DISPLAY"); got != "wayland-2" {
		t.Fatalf("WAYLAND_DISPLAY = %q, want wayland-2", got)
	}
	if got := environmentValue(wayland.fallback.commands.(*execClipboardCommands).env, "DISPLAY"); got != ":9" {
		t.Fatalf("fallback DISPLAY = %q, want :9", got)
	}
}

func TestNewClipboardBackendFallsBackWhenWaylandToolsMissing(t *testing.T) {
	runtimeDir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "wayland-0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	getenv := func(key string) string {
		switch key {
		case "WAYLAND_DISPLAY":
			return "wayland-0"
		case "XDG_RUNTIME_DIR":
			return runtimeDir
		}
		return ""
	}
	lookPath := func(string) (string, error) { return "", errors.New("not installed") }

	if backend := newClipboardBackendWith(":0", getenv, func() []string { return nil }, lookPath); backend.name() != "x11" {
		t.Fatalf("backend = %q, want x11 fallback", backend.name())
	}
}

func TestDetectWaylandDisplayFindsLiveSocket(t *testing.T) {
	runtimeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtimeDir, "wayland-stale"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "wayland-7"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	if got := detectWaylandDisplay("", runtimeDir); got != "wayland-7" {
		t.Fatalf("display = %q, want wayland-7", got)
	}
	if got := detectWaylandDisplay("nested-wayland", runtimeDir); got != "wayland-7" {
		t.Fatalf("stale explicit display fallback = %q, want wayland-7", got)
	}
}

func TestWaylandClipboardReadsAllSupportedContent(t *testing.T) {
	png := []byte("png bytes")
	commands := &fakeClipboardCommands{}
	commands.outputFunc = func(name string, args ...string) ([]byte, error) {
		if name != "wl-paste" {
			return nil, errors.New("unexpected command")
		}
		joined := strings.Join(args, " ")
		switch joined {
		case "--list-types":
			return []byte("text/plain\ntext/plain;charset=utf-8\nimage/png\ntext/uri-list\n"), nil
		case "--no-newline --type text/plain;charset=utf-8":
			return []byte("hello from Wayland"), nil
		case "--type image/png":
			return png, nil
		case "--type text/uri-list":
			return []byte("file:///tmp/a%20b.txt\r\n"), nil
		default:
			return nil, errors.New("unexpected arguments: " + joined)
		}
	}
	backend := testWaylandBackend(commands, "Hyprland")

	if got := backend.readText(); got != "hello from Wayland" {
		t.Fatalf("text = %q", got)
	}
	if got := backend.readImage(); !bytes.Equal(got, png) {
		t.Fatalf("image = %q, want %q", got, png)
	}
	if got := backend.readFiles(); !reflect.DeepEqual(got, []string{"/tmp/a b.txt"}) {
		t.Fatalf("files = %v", got)
	}
}

func TestWaylandClipboardWritesAllSupportedContent(t *testing.T) {
	commands := &fakeClipboardCommands{}
	backend := testWaylandBackend(commands, "Hyprland")

	if err := backend.writeText("hello"); err != nil {
		t.Fatal(err)
	}
	if err := backend.writeImage([]byte("png"), "image/png"); err != nil {
		t.Fatal(err)
	}
	if err := backend.writeFile("/tmp/a b.txt"); err != nil {
		t.Fatal(err)
	}

	want := []clipboardInputCall{
		{data: []byte("hello"), name: "wl-copy", args: []string{"--type", "text/plain;charset=utf-8"}},
		{data: []byte("png"), name: "wl-copy", args: []string{"--type", "image/png"}},
		{data: []byte("file:///tmp/a%20b.txt\r\n"), name: "wl-copy", args: []string{"--type", "text/uri-list"}},
	}
	if !reflect.DeepEqual(commands.inputs, want) {
		t.Fatalf("input calls = %#v, want %#v", commands.inputs, want)
	}
}

func TestWaylandClipboardWriteFailureFallsBackToX11(t *testing.T) {
	waylandCommands := &fakeClipboardCommands{
		inputFunc: func([]byte, string, ...string) error {
			return errors.New("Wayland selection unavailable")
		},
	}
	x11Commands := &fakeClipboardCommands{}
	backend := testWaylandBackend(waylandCommands, "Hyprland")
	backend.fallback = &x11Clipboard{commands: x11Commands}

	if err := backend.writeText("fallback text"); err != nil {
		t.Fatal(err)
	}
	if len(x11Commands.inputs) != 1 || x11Commands.inputs[0].name != "xclip" {
		t.Fatalf("X11 fallback calls = %#v, want one xclip call", x11Commands.inputs)
	}
}

func TestX11ClipboardKeepsXselTextFallback(t *testing.T) {
	commands := &fakeClipboardCommands{}
	commands.inputFunc = func(_ []byte, name string, _ ...string) error {
		if name == "xclip" {
			return errors.New("xclip unavailable")
		}
		return nil
	}
	backend := &x11Clipboard{commands: commands}

	if err := backend.writeText("fallback"); err != nil {
		t.Fatal(err)
	}
	if len(commands.inputs) != 2 || commands.inputs[1].name != "xsel" {
		t.Fatalf("input calls = %#v, want xclip then xsel", commands.inputs)
	}
}

func TestWaylandClipboardIntegration(t *testing.T) {
	if os.Getenv("MWB_WAYLAND_INTEGRATION") != "1" {
		t.Skip("set MWB_WAYLAND_INTEGRATION=1 inside a Wayland session")
	}
	backend := newClipboardBackend("")
	if backend.name() != "wayland" {
		t.Fatalf("backend = %q, want wayland", backend.name())
	}
	exerciseClipboardBackend(t, backend)
}

func TestX11ClipboardIntegration(t *testing.T) {
	if os.Getenv("MWB_X11_INTEGRATION") != "1" {
		t.Skip("set MWB_X11_INTEGRATION=1 inside an X11 session")
	}
	commands := &execClipboardCommands{
		env: replaceEnvironment(os.Environ(), "DISPLAY", os.Getenv("DISPLAY")),
	}
	backend := &x11Clipboard{commands: commands}
	exerciseClipboardBackend(t, backend)
}

func exerciseClipboardBackend(t *testing.T, backend clipboardBackend) {
	t.Helper()
	if err := backend.writeText("mwb clipboard text"); err != nil {
		t.Fatal(err)
	}
	waitForClipboard(t, func() bool { return backend.readText() == "mwb clipboard text" })

	imageData := testPNG(t)
	if err := backend.writeImage(imageData, "image/png"); err != nil {
		t.Fatal(err)
	}
	waitForClipboard(t, func() bool { return bytes.Equal(backend.readImage(), imageData) })

	path := filepath.Join(t.TempDir(), "a file.txt")
	if err := os.WriteFile(path, []byte("file body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backend.writeFile(path); err != nil {
		t.Fatal(err)
	}
	waitForClipboard(t, func() bool {
		return reflect.DeepEqual(backend.readFiles(), []string{path})
	})
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.NRGBA{R: 20, G: 80, B: 140, A: 255})
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func testWaylandBackend(commands clipboardCommands, desktop string) *waylandClipboard {
	return &waylandClipboard{
		commands:     commands,
		copyCommand:  "wl-copy",
		pasteCommand: "wl-paste",
		desktop:      desktop,
		fallback:     &x11Clipboard{commands: &fakeClipboardCommands{}},
	}
}

func environmentValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func waitForClipboard(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("clipboard selection did not become readable")
}
