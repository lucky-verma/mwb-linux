//go:build linux

package clipboard

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
)

func fileChannelPayload(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	header, err := filetransfer.EncodeHeader(filetransfer.Header{Size: int64(len(body)), Name: name})
	if err != nil {
		t.Fatal(err)
	}
	// The encrypted MWB stream carries whole AES blocks; the receiver discards
	// zero padding beyond the declared body size.
	paddedLen := (len(body) + 15) / 16 * 16
	payload := make([]byte, len(header)+paddedLen)
	copy(payload, header)
	copy(payload[len(header):], body)
	return payload
}

func TestHandleFileChannelStagesUntilPaste(t *testing.T) {
	root := filepath.Join(t.TempDir(), "hidden-cache", "mwb", "clipboard")
	m := NewManager(nil, "")
	m.stageRoot = root

	var published string
	m.setFileClipboard = func(path string) error {
		published = path
		return nil
	}

	body := []byte("remote file contents")
	res, err := m.HandleFileChannel(bytes.NewReader(fileChannelPayload(t, "proposal.docx", body)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path == "" || published != res.Path {
		t.Fatalf("published path = %q, result path = %q", published, res.Path)
	}
	if !strings.HasPrefix(res.Path, root+string(filepath.Separator)) {
		t.Fatalf("file was not staged below private root: %q", res.Path)
	}
	got, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("staged body = %q, want %q", got, body)
	}
}

func TestHandleFileChannelKeepsOnlyNewestClipboardItem(t *testing.T) {
	m := NewManager(nil, "")
	m.stageRoot = filepath.Join(t.TempDir(), "clipboard")
	m.setFileClipboard = func(string) error { return nil }

	first, err := m.HandleFileChannel(bytes.NewReader(fileChannelPayload(t, "first.txt", []byte("one"))), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.HandleFileChannel(bytes.NewReader(fileChannelPayload(t, "second.txt", []byte("two"))), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("previous staged file still exists: %v", err)
	}
	if _, err := os.Stat(second.Path); err != nil {
		t.Fatalf("newest staged file missing: %v", err)
	}
}

func TestHandleFileChannelPublishFailureLeavesNoNewFile(t *testing.T) {
	m := NewManager(nil, "")
	m.stageRoot = filepath.Join(t.TempDir(), "clipboard")
	m.setFileClipboard = func(string) error { return errors.New("selection owner unavailable") }

	_, err := m.HandleFileChannel(bytes.NewReader(fileChannelPayload(t, "failed.txt", []byte("data"))), 0)
	if err == nil {
		t.Fatal("expected publish error")
	}
	entries, readErr := os.ReadDir(m.stageRoot)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("failed transfer left %d staged entries", len(entries))
	}
}

func TestFileClipboardPayloadEscapesPath(t *testing.T) {
	got := fileClipboardPayload("/tmp/a b#c.txt", uriListTarget)
	if got != "file:///tmp/a%20b%23c.txt\r\n" {
		t.Fatalf("payload = %q", got)
	}
}

func TestGNOMEFileClipboardPayload(t *testing.T) {
	got := fileClipboardPayload("/tmp/a b.txt", gnomeFileTarget)
	if got != "copy\nfile:///tmp/a%20b.txt" {
		t.Fatalf("payload = %q", got)
	}
	if target := preferredFileClipboardTarget("ubuntu:GNOME"); target != gnomeFileTarget {
		t.Fatalf("GNOME target = %q", target)
	}
	if target := preferredFileClipboardTarget("KDE"); target != uriListTarget {
		t.Fatalf("KDE target = %q", target)
	}
}
