//go:build linux

package clipboard

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Some Wayland compositors keep the native clipboard and the XWayland one
// apart until an X11 window has focus: KDE Plasma copies a Wayland app's
// selection to X11 only then. xclip, which mwb polls, never sees those
// copies. Compositors that offer a data-control protocol (KDE, wlroots
// desktops such as Sway and Hyprland) let a background client read the
// native clipboard; wl-paste speaks it. GNOME offers no data-control
// protocol, but it mirrors the clipboard to XWayland eagerly, so xclip
// already works there.
//
// Writing stays on X11 (xclip and the file selection owner): compositors
// mirror an X11 selection into the native clipboard as soon as it is set.

var dataControlProtocols = []string{"ext_data_control_manager_v1", "zwlr_data_control_manager_v1"}

// waylandSocket returns the compositor socket path, or "" outside Wayland.
func waylandSocket() string {
	name := os.Getenv("WAYLAND_DISPLAY")
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		return name
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// hasDataControl asks the compositor at socket whether it offers a
// data-control protocol, by listing its globals.
func hasDataControl(socket string) (bool, error) {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// wl_display (object 1): get_registry (opcode 1) -> new object 2, then
	// sync (opcode 0) -> callback 3, whose done event ends the global list.
	req := make([]byte, 0, 24)
	for _, m := range [][3]uint32{{1, 1, 2}, {1, 0, 3}} {
		req = binary.LittleEndian.AppendUint32(req, m[0])
		req = binary.LittleEndian.AppendUint32(req, 12<<16|m[1])
		req = binary.LittleEndian.AppendUint32(req, m[2])
	}
	if _, err := conn.Write(req); err != nil {
		return false, err
	}
	globals, err := readGlobals(conn)
	if err != nil {
		return false, err
	}
	for _, g := range globals {
		for _, p := range dataControlProtocols {
			if g == p {
				return true, nil
			}
		}
	}
	return false, nil
}

// readGlobals reads wl_registry.global events (object 2) until the sync
// callback (object 3) fires, and returns the interface names.
func readGlobals(r io.Reader) ([]string, error) {
	var names []string
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil, err
		}
		obj := binary.LittleEndian.Uint32(hdr[0:4])
		sizeOp := binary.LittleEndian.Uint32(hdr[4:8])
		size, op := int(sizeOp>>16), sizeOp&0xffff
		if size < 8 || size > 4096 {
			return nil, errors.New("malformed Wayland message")
		}
		body := make([]byte, size-8)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, err
		}
		switch {
		case obj == 3: // wl_callback.done
			return names, nil
		case obj == 1 && op == 0: // wl_display.error
			return nil, errors.New("compositor reported an error")
		case obj == 2 && op == 0 && len(body) >= 8: // wl_registry.global
			n := int(binary.LittleEndian.Uint32(body[4:8])) // length incl. NUL
			if n < 1 || 8+n > len(body) {
				return nil, errors.New("malformed global")
			}
			names = append(names, string(body[8:8+n-1]))
		}
	}
}

// useWaylandRead reports whether the local clipboard should be read with
// wl-paste, and warns once when it should but wl-paste is missing.
func useWaylandRead() bool {
	socket := waylandSocket()
	if socket == "" {
		return false
	}
	ok, err := hasDataControl(socket)
	if err != nil || !ok {
		return false
	}
	if _, err := exec.LookPath("wl-paste"); err != nil {
		slog.Warn("this desktop keeps Wayland apps' clipboard from X11 apps; install wl-clipboard so copies made there are shared",
			"fix", "sudo apt install wl-clipboard")
		return false
	}
	slog.Info("reading the clipboard with wl-paste (Wayland data control)")
	return true
}

// wlPaste reads one type from the native Wayland clipboard; "" lists the
// offered types, one per line, like xclip's TARGETS.
func wlPaste(target string) ([]byte, error) {
	args := []string{"--no-newline"}
	if target == "" {
		args = []string{"--list-types"}
	} else {
		args = append(args, "--type", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "wl-paste", args...).Output()
}
