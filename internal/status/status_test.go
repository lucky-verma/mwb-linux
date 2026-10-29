// internal/status/status_test.go
package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathRequiresAbsoluteRuntimeDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	if p := Path(); p != "" {
		t.Fatalf("Path() = %q with no runtime dir, want empty (never a shared fallback)", p)
	}
	t.Setenv("XDG_RUNTIME_DIR", "relative/dir")
	if p := Path(); p != "" {
		t.Fatalf("Path() = %q for a relative runtime dir, want empty", p)
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if p := Path(); p != "/run/user/1000/mwb/status.json" {
		t.Fatalf("Path() = %q", p)
	}
}

func TestWriterLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "status.json")
	w := NewWriter(path, Status{Peer: "linux", Host: "10.0.0.2", PID: 42, Version: "dev"})

	if err := w.Connecting(); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if got.State != StateConnecting || got.Remote != "" || got.Peer != "linux" || got.PID != 42 {
		t.Fatalf("after Connecting: %+v", got)
	}

	if err := w.Connected("desk", true); err != nil {
		t.Fatal(err)
	}
	got = read(t, path)
	if got.State != StateConnected || got.Remote != "desk" || !got.Outbound || got.Host != "10.0.0.2" {
		t.Fatalf("after Connected: %+v", got)
	}

	// Reconnecting clears the old remote rather than showing it as current.
	if err := w.Connecting(); err != nil {
		t.Fatal(err)
	}
	if got = read(t, path); got.Remote != "" || got.Outbound {
		t.Fatalf("stale remote after reconnect: %+v", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("status file mode %o is readable by other users", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("status dir mode %o, want 700", perm)
	}

	w.Remove()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("status file still present after Remove: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".status-") {
			t.Fatalf("temporary file %s left behind", e.Name())
		}
	}
}

func TestEmptyPathWriterIsNoOp(t *testing.T) {
	w := NewWriter("", Status{})
	if err := w.Connected("x", false); err != nil {
		t.Fatal(err)
	}
	w.Remove()
	var nilWriter *Writer
	if err := nilWriter.Connecting(); err != nil {
		t.Fatal(err)
	}
	nilWriter.Remove()
}

func TestKeyMismatchRecordedThenClearedOnConnect(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "status.json")
	w := NewWriter(path, Status{Peer: "linux", Waiting: true})

	if err := w.KeyMismatch("10.0.0.9:52100"); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if got.KeyMismatchFrom != "10.0.0.9:52100" || got.KeyMismatchAt.IsZero() {
		t.Fatalf("after KeyMismatch: %+v", got)
	}

	// A real connection means the keys now match: the warning must clear.
	if err := w.Connected("10.0.0.9", false); err != nil {
		t.Fatal(err)
	}
	if got = read(t, path); got.KeyMismatchFrom != "" || !got.KeyMismatchAt.IsZero() {
		t.Fatalf("key mismatch not cleared after Connected: %+v", got)
	}
}

func read(t *testing.T, path string) Status {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPeerFieldsAndSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwb", "status.json")
	w := NewWriter(path, Status{Peer: "linux"})
	if err := w.Connected("laptop", false); err != nil {
		t.Fatal(err)
	}
	since := read(t, path).Since
	if err := w.LocalSharing(true); err != nil {
		t.Fatal(err)
	}
	if err := w.Peer(1920, 1080, "right", false); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	if got.Sharing == nil || !*got.Sharing || got.PeerSharing == nil || *got.PeerSharing ||
		got.PeerScreenWidth != 1920 || got.PeerEdge != "right" {
		t.Fatalf("peer fields: %+v", got)
	}
	if !got.Since.Equal(since) {
		t.Fatal("Since changed although the state did not")
	}
	// Reconnecting forgets the old peer.
	if err := w.Connecting(); err != nil {
		t.Fatal(err)
	}
	if got = read(t, path); got.PeerSharing != nil || got.Sharing != nil || got.PeerScreenWidth != 0 || got.PeerEdge != "" {
		t.Fatalf("stale peer fields after reconnect: %+v", got)
	}
}
