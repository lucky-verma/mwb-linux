// internal/network/filesend.go
package network

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/filetransfer"
)

// fileWriteIdleTimeout bounds a single write to the file channel.
//
// The transfer as a whole stays unbounded on purpose: 100 MB over a slow link
// is legitimate, and a fixed total deadline would abort it. What has to be
// bounded is a peer that stops reading. The sending goroutine is tracked by the
// clipboard manager's WaitGroup, Stop waits on that WaitGroup, and the
// reconnect loop waits on Stop — so a wedged peer would otherwise hold the
// reconnect loop for as long as TCP takes to give up, which is minutes.
const fileWriteIdleTimeout = 60 * time.Second

// idleWriter refreshes the write deadline ahead of every write, so it is
// progress rather than total elapsed time that keeps a transfer alive.
type idleWriter struct {
	conn    net.Conn  // carries the deadline
	w       io.Writer // carries the bytes
	timeout time.Duration
}

func (iw *idleWriter) Write(p []byte) (int, error) {
	if err := iw.conn.SetWriteDeadline(time.Now().Add(iw.timeout)); err != nil {
		return 0, fmt.Errorf("set file write deadline: %w", err)
	}
	return iw.w.Write(p)
}

// FileSender copies locally copied files to the peer. It uses the control
// connection's identity and key, but dials the peer's base/clipboard port and
// opens with the raw header expected by Clipboard.ShakeHand.
type FileSender struct {
	Addr        string // peer host:clipboard-port
	SecurityKey string
	MachineName string // our name, as the peer should see it
	MachineID   uint32 // the control connection's ID, echoed in the channel header
	MaxSize     int64  // 0 selects MWB's own 100 MB cap
	DialTimeout time.Duration
}

// Send transfers a clipboard file selection to the peer.
//
// MWB moves one file per copy, so a multi-file selection is reported and
// skipped rather than being silently reduced to an arbitrary member of it.
// Skipping is not an error: copying several files at once is a normal thing to
// do, it simply has no representation in the protocol.
func (fs FileSender) Send(paths []string) error {
	switch {
	case len(paths) == 0:
		return nil
	case len(paths) > 1:
		slog.Info("file copy skipped: MWB transfers one file at a time, zip a multi-file selection first",
			"count", len(paths))
		return nil
	}
	path := paths[0]

	// Gate before dialing. Copying a folder is common, and it should not cost
	// the peer a file channel that it only ever sees opened and abandoned.
	if _, err := filetransfer.Sendable(path, fs.MaxSize); err != nil {
		return err
	}

	conn, err := DialFile(fs.Addr, fs.SecurityKey, fs.MachineName, fs.MachineID, fs.DialTimeout)
	if err != nil {
		return err
	}
	// A file channel ends at EOF. Do not use Conn.Close: it writes a stamped
	// control-channel ByeBye packet, which is not part of this stream.
	defer func() { _ = conn.raw.Close() }()

	w := &idleWriter{conn: conn.raw, w: conn.enc, timeout: fileWriteIdleTimeout}
	return filetransfer.Send(w, path, fs.MaxSize)
}
