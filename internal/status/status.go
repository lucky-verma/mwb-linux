// internal/status/status.go

// Package status publishes the running client's connection state to a small
// file, so a desktop front end (mwb-gui) can show it without opening a socket
// or parsing logs.
//
// The file lives under $XDG_RUNTIME_DIR, which systemd creates per user with
// mode 0700 on a tmpfs, so it never outlives the login session and other
// accounts cannot read it. If XDG_RUNTIME_DIR is unset nothing is written:
// falling back to a shared directory such as /tmp would let another local user
// plant or read the file.
//
// The file holds no secrets: never the security key.
package status

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/group"
)

// States written to the file.
const (
	StateConnecting = "connecting"
	StateConnected  = "connected"
	// StateNotConfigured: no config file yet. mwb waits for one and starts
	// as soon as it is saved.
	StateNotConfigured = "not_configured"
	// StateConfigError: the config file cannot be used; Error says why.
	StateConfigError = "config_error"
	// StateNoNetwork: the configured interface has no address yet.
	StateNoNetwork = "no_network"
)

// Status is the JSON document mwb-gui reads.
type Status struct {
	State         string   `json:"state"`
	Peer          string   `json:"peer"`
	Host          string   `json:"host"`
	Waiting       bool     `json:"waiting,omitempty"`   // a Linux peer accepting, not dialing
	Addresses     []string `json:"addresses,omitempty"` // where the other machine can reach this one
	Remote        string   `json:"remote,omitempty"`
	Outbound      bool     `json:"outbound,omitempty"`
	Bidirectional bool     `json:"bidirectional"`
	Edge          string   `json:"edge"`
	// EdgeFrom names the Linux peer whose more recent side choice this
	// machine adopted, mirrored; empty when the side is this machine's own.
	EdgeFrom string `json:"edge_from,omitempty"`
	// Sharing is whether this machine actually forwards its input: requested
	// and capture started. Known once connected.
	Sharing *bool `json:"sharing,omitempty"`
	// Peer* come from a Linux peer's LinuxPeerInfo; absent until it arrives.
	PeerScreenWidth  int32  `json:"peer_screen_width,omitempty"`
	PeerScreenHeight int32  `json:"peer_screen_height,omitempty"`
	PeerEdge         string `json:"peer_edge,omitempty"`
	PeerSharing      *bool  `json:"peer_sharing,omitempty"`
	Clipboard        bool   `json:"clipboard"`
	// Group state, for peer = "linux": the screens left to right, the other
	// machines, and which machine has the cursor ("" = unknown).
	Group    bool           `json:"group,omitempty"`
	Layout   []string       `json:"layout,omitempty"`
	Members  []group.Member `json:"members,omitempty"`
	Location string         `json:"location,omitempty"`
	// Pos is the two-dimensional arrangement (column and row per machine) when
	// the group has one; absent for a plain row. mwb-gui draws from it.
	Pos map[string]group.Pos `json:"pos,omitempty"`
	// LayoutBy names the machine whose screen order is in effect. mwb-gui
	// shows it when another machine set the order, so a shared edge that moved
	// to the far side does not look like a broken crossing.
	LayoutBy string    `json:"layout_by,omitempty"`
	PID      int       `json:"pid"`
	Version  string    `json:"version"`
	Since    time.Time `json:"since"`
	// Error explains StateConfigError and StateNoNetwork.
	Error string `json:"error,omitempty"`
	// Interface is the network interface mwb is restricted to, if any.
	Interface string `json:"interface,omitempty"`
	// KeyMismatchFrom and KeyMismatchAt record the last time a computer tried
	// to connect with a different security key. mwb-gui shows this while the
	// machine is not yet connected, so a key typed wrong looks like the real
	// misconfiguration it is instead of a silent "not connected". Cleared once
	// a connection is established.
	KeyMismatchFrom string    `json:"key_mismatch_from,omitempty"`
	KeyMismatchAt   time.Time `json:"key_mismatch_at,omitempty"`
}

// Path returns the status file location, or "" when XDG_RUNTIME_DIR is unset.
func Path() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" || !filepath.IsAbs(dir) {
		return ""
	}
	return filepath.Join(dir, "mwb", "status.json")
}

// Writer publishes updates to one status file. The zero value is not usable;
// use NewWriter. A Writer with an empty path discards everything.
type Writer struct {
	mu   sync.Mutex
	path string
	base Status
}

// NewWriter returns a writer for path, using base for the fields that do not
// change during the process lifetime. An empty path yields a no-op writer.
func NewWriter(path string, base Status) *Writer {
	return &Writer{path: path, base: base}
}

// Connecting records that the client is waiting for its peer.
func (w *Writer) Connecting() error {
	return w.write(func(s *Status) {
		s.State = StateConnecting
		s.Remote = ""
		s.Outbound = false
		s.Sharing = nil
		s.PeerScreenWidth, s.PeerScreenHeight, s.PeerEdge, s.PeerSharing = 0, 0, "", nil
	})
}

// Connected records an established control connection.
func (w *Writer) Connected(remote string, outbound bool) error {
	return w.write(func(s *Status) {
		s.State = StateConnected
		s.Remote = remote
		s.Outbound = outbound
		s.KeyMismatchFrom, s.KeyMismatchAt = "", time.Time{}
	})
}

// LocalSharing records whether this machine forwards its input on this
// connection.
func (w *Writer) LocalSharing(on bool) error {
	return w.write(func(s *Status) { s.Sharing = &on })
}

// Peer records the settings a Linux peer reported about itself.
func (w *Writer) Peer(width, height int32, edge string, sharing bool) error {
	return w.write(func(s *Status) {
		s.PeerScreenWidth, s.PeerScreenHeight = width, height
		s.PeerEdge = edge
		s.PeerSharing = &sharing
	})
}

// AdoptedEdge records that this machine switched to edge to mirror the side
// the peer named from chose more recently. It lasts for the process lifetime.
func (w *Writer) AdoptedEdge(edge, from string) error {
	return w.write(func(s *Status) { s.Edge, s.EdgeFrom = edge, from })
}

// Group records a group's state. The connection counts as up while any other
// machine is online, and Remote lists the ones that are.
func (w *Writer) Group(snap group.Snapshot) error {
	return w.write(func(s *Status) {
		s.Group = true
		s.Layout, s.Members, s.Location = snap.Layout, snap.Members, snap.Location
		s.LayoutBy = snap.LayoutBy
		s.Pos = snap.Pos
		var online []string
		for _, m := range snap.Members {
			if m.Online {
				online = append(online, m.Name)
			}
		}
		if len(online) > 0 {
			s.State = StateConnected
			s.Remote = strings.Join(online, ", ")
			s.KeyMismatchFrom, s.KeyMismatchAt = "", time.Time{}
		} else {
			s.State = StateConnecting
			s.Remote = ""
		}
	})
}

// Waiting records that mwb cannot start yet: state is StateNotConfigured,
// StateConfigError or StateNoNetwork, and reason explains it.
func (w *Writer) Waiting(state, reason string) error {
	return w.write(func(s *Status) { s.State, s.Error = state, reason })
}

// KeyMismatch records that a computer at remote tried to connect with a
// different security key. The GUI shows it while this machine is not yet
// connected.
func (w *Writer) KeyMismatch(remote string) error {
	return w.write(func(s *Status) {
		s.KeyMismatchFrom = remote
		s.KeyMismatchAt = time.Now().UTC().Truncate(time.Second)
	})
}

// Remove deletes the status file on clean shutdown. A crash leaves it behind;
// readers detect that through the recorded PID.
func (w *Writer) Remove() {
	if w == nil || w.path == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = os.Remove(w.path)
}

func (w *Writer) write(update func(*Status)) error {
	if w == nil || w.path == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	s := w.base
	update(&s)
	// Since marks the last change of state, not of any field.
	if s.State != w.base.State || s.Since.IsZero() {
		s.Since = time.Now().UTC().Truncate(time.Second)
	}
	w.base = s

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create status dir: %w", err)
	}
	// Write then rename, so a reader never sees a half-written file.
	tmp, err := os.CreateTemp(dir, ".status-*.json")
	if err != nil {
		return fmt.Errorf("create status file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write status file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close status file: %w", err)
	}
	if err := os.Rename(tmpName, w.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("publish status file: %w", err)
	}
	return nil
}
