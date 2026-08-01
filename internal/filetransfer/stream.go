package filetransfer

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// Result describes a completed inbound transfer.
type Result struct {
	Path   string // final path on disk, empty for an inline payload
	Name   string // sanitised name
	Size   int64  // bytes actually written
	Inline []byte // populated instead of Path when the payload was clipboard content
}

// Receive reads one file from an already-decrypted stream and writes it into
// dir.
//
// The declared size in the header is never trusted. The body is copied through
// a limited reader capped at maxSize, so a peer that under-declares cannot
// write more than the cap, and a peer that over-declares is rejected up front.
//
// Content is written to a ".part" file and renamed only after the full declared
// size arrives, so an interrupted transfer never leaves behind a file that
// looks complete.
func Receive(r io.Reader, dir string, maxSize int64) (*Result, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}

	raw := make([]byte, HeaderSize)
	if err := readFull(r, raw); err != nil {
		return nil, err
	}
	hdr, err := ParseHeader(raw, maxSize)
	if err != nil {
		return nil, err
	}

	// MWB reuses this channel for oversized clipboard text and images. Those
	// belong in memory, not on disk.
	if IsInlinePayload(hdr.Name) {
		// Deliberately not sized from hdr.Size. The declared size is peer
		// controlled, so honouring it here lets a peer force a large allocation
		// while sending nothing, multiplied by the number of connections the
		// accept path allows. Let append grow against real data instead.
		body := make([]byte, 0, inlineInitialCap)
		buf := &limitedBuffer{buf: &body, limit: maxSize}
		if _, err := readAligned(buf, r, hdr.Size); err != nil {
			return nil, fmt.Errorf("read inline payload: %w", err)
		}
		return &Result{Name: hdr.Name, Size: int64(len(body)), Inline: body}, nil
	}

	name, err := SafeName(hdr.Name)
	if err != nil {
		return nil, err
	}

	// DestPath creates dir; the space check must run against a directory that
	// exists or statfs fails and the check silently passes on first use.
	final, err := DestPath(dir, name)
	if err != nil {
		return nil, err
	}
	if err := checkFreeSpace(filepath.Dir(final), hdr.Size); err != nil {
		return nil, err
	}

	part := final + ".part"
	// O_EXCL so an attacker cannot pre-create the .part path as a symlink and
	// have the write follow it. 0600 because the sender is remote and the
	// content is not ours to share; never executable.
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", part, err)
	}

	written, copyErr := readAligned(f, r, hdr.Size)
	closeErr := f.Close()

	if copyErr != nil {
		removePartial(part)
		return nil, fmt.Errorf("write %s: %w", part, copyErr)
	}
	if closeErr != nil {
		removePartial(part)
		return nil, fmt.Errorf("close %s: %w", part, closeErr)
	}
	if written != hdr.Size {
		removePartial(part)
		return nil, fmt.Errorf("%w: got %d of %d bytes", ErrShortBody, written, hdr.Size)
	}

	if err := os.Rename(part, final); err != nil {
		removePartial(part)
		return nil, fmt.Errorf("finalise %s: %w", final, err)
	}

	slog.Info("received file", "path", final, "bytes", written)
	return &Result{Path: final, Name: filepath.Base(final), Size: written}, nil
}

// Sendable reports the size of a file that may be sent, rejecting everything
// MWB does not transfer: directories, non-regular files, and content past the
// cap.
//
// Exported separately from Send so a caller can reject a selection before
// opening a connection. A folder copy should not cost the peer a file channel
// that it only ever sees abandoned.
func Sendable(path string, maxSize int64) (int64, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		// MWB does not transfer directories either; the documented workaround
		// is to zip them first.
		return 0, fmt.Errorf("%w: %s is a directory", ErrUnsafeName, path)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("%w: %s is not a regular file", ErrUnsafeName, path)
	}
	if info.Size() > maxSize {
		return 0, fmt.Errorf("%w: %s is %d bytes, limit is %d", ErrSizeRejected, path, info.Size(), maxSize)
	}
	return info.Size(), nil
}

// Send writes one file to an already-encrypted stream.
//
// The size is re-read here rather than taken from the caller: an early
// Sendable check is a gate, not a guarantee, and the file may have changed
// between the two.
func Send(w io.Writer, path string, maxSize int64) error {
	size, err := Sendable(path, maxSize)
	if err != nil {
		return err
	}

	hdr, err := EncodeHeader(Header{Size: size, Name: filepath.Base(path)})
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	if _, err := w.Write(hdr); err != nil {
		return fmt.Errorf("send header: %w", err)
	}

	// Bound the copy by the size already announced. A file growing mid-transfer
	// must not desynchronise the peer, which stops reading at the declared size.
	sent, err := writeAligned(w, f, size)
	if err != nil {
		return fmt.Errorf("send body: %w", err)
	}
	if sent != size {
		return fmt.Errorf("%w: sent %d of %d bytes", ErrShortBody, sent, size)
	}

	slog.Info("sent file", "path", path, "bytes", sent)
	return nil
}

func removePartial(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("could not remove partial transfer", "path", path, "err", err)
	}
}

// inlineInitialCap is a starting capacity for an inline payload. Growth is
// driven by bytes actually received, never by the peer's declared size.
const inlineInitialCap = 32 * 1024

// limitedBuffer accumulates an inline payload while refusing to grow past limit.
type limitedBuffer struct {
	buf   *[]byte
	limit int64
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if int64(len(*l.buf))+int64(len(p)) > l.limit {
		return 0, fmt.Errorf("%w: inline payload exceeds %d bytes", ErrSizeRejected, l.limit)
	}
	*l.buf = append(*l.buf, p...)
	return len(p), nil
}

// EffectiveMaxSize resolves a configured cap, substituting the MWB default when
// unset so callers and logs agree on the number actually enforced.
func EffectiveMaxSize(configured int64) int64 {
	if configured <= 0 {
		return DefaultMaxSize
	}
	return configured
}
