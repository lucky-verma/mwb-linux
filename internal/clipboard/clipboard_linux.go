//go:build linux

// Package clipboard implements MWB clipboard sharing between Linux and Windows.
// Text clipboard data is UTF-16 encoded, Deflate compressed, and sent in 48-byte
// chunks as ClipboardText (124) packets, terminated by ClipboardDataEnd (76).
package clipboard

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lucky-verma/mwb-linux/internal/network"
	"github.com/lucky-verma/mwb-linux/internal/protocol"
)

const (
	dataSize      = 48 // bytes of clipboard data per 64-byte packet
	pollInterval  = 1 * time.Second
	execTimeout   = 5 * time.Second // max time for any xclip/xsel call
	textTypeSep   = "{4CFF57F7-BEDD-43d5-AE8F-27A61E886F2F}"
	maxInlineSize = 1048576     // 1 MB — max for inline TCP send
	maxRecvBuf    = 2 * 1048576 // 2 MB — max in-flight clipboard receive buffer
	// PowerToys remembers a large/file clipboard beat for 30 seconds and pulls
	// it only when the cursor switches onto the receiving machine. Mirroring
	// that timing avoids downloading every Windows copy immediately.
	remoteClipboardTimeout = 30 * time.Second

	// maxDecompressedSize caps the inflated size of an inbound clipboard payload.
	// maxRecvBuf only bounds the *compressed* bytes; DEFLATE can expand ~1000:1,
	// so without an output cap a few MB of crafted data can inflate to gigabytes
	// and exhaust memory (decompression bomb). 64 MiB accommodates large,
	// high-resolution clipboard images while bounding worst-case allocation.
	maxDecompressedSize = 64 * 1048576
)

var errDecompressedTooLarge = errors.New("decompressed clipboard exceeds size limit")

// Manager handles clipboard synchronization.
type Manager struct {
	// OnFileCopy is invoked when the local clipboard holds a file selection.
	// File bytes do not travel over the clipboard packet stream, so the actual
	// transfer belongs to the file channel; nil disables file sending.
	OnFileCopy func(paths []string)

	conn        *network.Conn
	peers       func() []*network.Conn // group mode: every member online now
	display     string
	wayland     bool   // read with wl-paste: the native clipboard is not mirrored to X11
	lastHash    string // hash of last clipboard content we sent
	clipGen     int64  // bumped by the XFIXES watcher on each CLIPBOARD copy
	mu          sync.Mutex
	recvBuf     bytes.Buffer // accumulates incoming clipboard chunks
	receiving   bool
	recvIsImage bool
	justSet     time.Time // when we last set clipboard from remote — suppress re-send
	pendingPull uint32    // remote ID whose large/file clipboard is waiting
	pendingAt   time.Time // when the pending Clipboard beat arrived
	stopCh      chan struct{}
	wg          sync.WaitGroup // tracks pollClipboard goroutine for clean shutdown

	// Inbound files are private clipboard backing storage, not downloads. The
	// file manager copies from this hidden path only when the user pastes.
	fileMu           sync.Mutex
	stageRoot        string
	setFileClipboard func(string) error
}

// NewManager creates a clipboard manager.
func NewManager(conn *network.Conn, display string) *Manager {
	m := &Manager{
		conn:    conn,
		display: display,
		stopCh:  make(chan struct{}),
	}
	m.stageRoot = defaultStageRoot()
	m.setFileClipboard = m.setLocalFileClipboard
	return m
}

// NewGroupManager creates a clipboard manager that shares with every machine
// peers returns, for a group of mwb-linux machines.
func NewGroupManager(peers func() []*network.Conn, display string) *Manager {
	m := NewManager(nil, display)
	m.peers = peers
	return m
}

// conns returns where clipboard changes go.
func (m *Manager) conns() []*network.Conn {
	if m.peers != nil {
		return m.peers()
	}
	if m.conn != nil {
		return []*network.Conn{m.conn}
	}
	return nil
}

// broadcast sends pkts, in order, over every connection, each stamped with
// that connection's machine ID. It reports whether every send succeeded.
func (m *Manager) broadcast(pkts []*protocol.Packet) bool {
	ok := true
	for _, conn := range m.conns() {
		for _, tmpl := range pkts {
			p := *tmpl
			p.Src = conn.MachineID
			if err := conn.SendPacket(&p); err != nil {
				slog.Error("send clipboard failed", "remote", conn.RemoteName, "err", err)
				ok = false
				break
			}
		}
	}
	return ok
}

// Start begins monitoring the local clipboard for changes.
func (m *Manager) Start() {
	// A connection/restart is not a clipboard change. Baseline the selection
	// already owned by Linux so it is not pushed to Windows as if the user had
	// just copied it. Only changes observed after Start are transmitted.
	m.wayland = useWaylandRead()
	m.seedLocalClipboardHash()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.pollClipboard()
	}()
	// Detect a repeat copy of the same file: the path alone cannot tell a fresh
	// copy from one that is simply still on the clipboard, so watch the X
	// selection for ownership changes. Degrades to path comparison if XFIXES is
	// unavailable.
	if m.OnFileCopy != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.watchSelectionChanges()
		}()
	}
	slog.Info("clipboard sharing enabled")
}

// Stop stops clipboard monitoring and waits for the goroutine to exit.
func (m *Manager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
}

// HandlePacket processes incoming clipboard packets.
func (m *Manager) HandlePacket(pkt *protocol.Packet) {
	switch pkt.Type {
	case protocol.ClipboardText, protocol.ClipboardImage:
		m.clearPendingRemoteClipboard()
		m.handleChunk(pkt)
	case protocol.ClipboardDataEnd:
		m.handleEnd(pkt)
	case protocol.Clipboard:
		m.rememberRemoteClipboard(pkt)
	case protocol.ClipboardAsk:
		slog.Debug("clipboard ask received — sending current clipboard")
		go m.sendClipboard()
	default:
		slog.Debug("unhandled clipboard packet", "type", pkt.Type)
	}
}

// rememberRemoteClipboard records MWB's clipboard beat without pulling it yet.
//
// PowerToys sends a beat only when the clipboard content is not already sent
// inline: usually a copied file or text/image larger than its inline threshold.
// Official MWB does not answer here. It remembers the source for 30 seconds and
// asks only when the cursor switches to the receiving machine.
func (m *Manager) rememberRemoteClipboard(beat *protocol.Packet) {
	remoteID := beat.Src
	if remoteID == 0 && m.conn != nil {
		remoteID = m.conn.RemoteID
	}
	if remoteID == 0 {
		slog.Warn("clipboard beat has no resolvable source")
		return
	}

	m.mu.Lock()
	m.pendingPull = remoteID
	m.pendingAt = time.Now()
	m.mu.Unlock()
	slog.Info("remote clipboard waiting for Linux activation", "remoteID", remoteID)
}

// HandleActivation pulls the pending remote clipboard when the cursor arrives
// on Linux. This is the same trigger PowerToys uses for large clipboard data.
func (m *Manager) HandleActivation() {
	m.mu.Lock()
	remoteID := m.pendingPull
	pendingAt := m.pendingAt
	m.pendingPull = 0
	m.pendingAt = time.Time{}
	m.mu.Unlock()

	if remoteID == 0 {
		return
	}
	if time.Since(pendingAt) > remoteClipboardTimeout {
		slog.Debug("pending remote clipboard expired", "remoteID", remoteID)
		return
	}
	if m.conn == nil {
		slog.Warn("cannot request remote clipboard without a control connection")
		return
	}

	ask := &protocol.Packet{
		Type:       protocol.ClipboardAsk,
		Src:        m.conn.MachineID,
		Des:        remoteID,
		PostAction: protocol.PostActionOther,
	}
	ask.SetMachineName(m.conn.LocalName)
	if err := m.conn.SendPacket(ask); err != nil {
		slog.Error("request remote clipboard failed", "remoteID", remoteID, "err", err)
		return
	}
	slog.Info("requested remote clipboard over file channel", "remoteID", remoteID)
}

func (m *Manager) clearPendingRemoteClipboard() {
	m.mu.Lock()
	m.pendingPull = 0
	m.pendingAt = time.Time{}
	m.mu.Unlock()
}

// seedLocalClipboardHash records the current selection without sending it.
// PowerToys reacts to clipboard-change events, not whatever happened to be on
// a peer's clipboard when a TCP connection was established.
func (m *Manager) seedLocalClipboardHash() {
	var hash string
	m.mu.Lock()
	gen := m.clipGen
	m.mu.Unlock()
	if m.OnFileCopy != nil {
		if paths := m.getLocalFileClipboard(); len(paths) > 0 {
			hash = fileClipHash(gen, paths)
		}
	}
	if hash == "" {
		if imgData := m.getLocalImageClipboard(); imgData != nil {
			hash = fmt.Sprintf("img:%d", len(imgData))
		}
	}
	if hash == "" {
		if text := m.getLocalClipboard(); text != "" {
			hash = fmt.Sprintf("%d:%s", len(text), text[:min(100, len(text))])
		}
	}

	m.mu.Lock()
	// Incoming clipboard data cannot normally race Start (the receive loop has
	// not begun), but do not overwrite it if a caller uses Manager differently.
	if m.lastHash == "" {
		m.lastHash = hash
	}
	m.mu.Unlock()
	slog.Debug("initial Linux clipboard baselined", "hasContent", hash != "")
}

// fileClipHash identifies a file selection for change detection. The copy
// generation (from the XFIXES watcher) is part of it so that re-copying the
// same file — same paths, new ownership — is seen as new rather than as the
// unchanged clipboard still sitting there. With XFIXES unavailable the
// generation stays 0 and this reduces to the paths alone.
func fileClipHash(gen int64, paths []string) string {
	return fmt.Sprintf("file:%d:%s", gen, strings.Join(paths, "\x00"))
}

// pollClipboard monitors the local clipboard for changes.
func (m *Manager) pollClipboard() {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			// Don't re-send clipboard we just received from remote
			m.mu.Lock()
			recentlySet := time.Since(m.justSet) < 3*time.Second
			m.mu.Unlock()
			if recentlySet {
				continue
			}

			// A copied file also offers text/plain holding its path, so files
			// must be checked before text or the path gets sent as a string.
			if m.OnFileCopy != nil {
				if paths := m.getLocalFileClipboard(); len(paths) > 0 {
					m.mu.Lock()
					hash := fileClipHash(m.clipGen, paths)
					changed := hash != m.lastHash
					if changed {
						m.lastHash = hash
					}
					m.mu.Unlock()
					if changed {
						slog.Info("file clipboard changed, sending to remote", "count", len(paths))
						m.wg.Add(1)
						go func(p []string) {
							defer m.wg.Done()
							m.OnFileCopy(p)
						}(paths)
					}
					continue
				}
			}

			// Check for image clipboard first
			imgData := m.getLocalImageClipboard()
			if imgData != nil {
				hash := fmt.Sprintf("img:%d", len(imgData))
				m.mu.Lock()
				changed := hash != m.lastHash
				if changed {
					m.lastHash = hash
				}
				m.mu.Unlock()
				if changed {
					slog.Info("image clipboard changed, sending to remote", "size", len(imgData))
					m.wg.Add(1)
					go func(d []byte) {
						defer m.wg.Done()
						m.sendImage(d)
					}(imgData)
				}
				continue
			}

			// Check for text clipboard
			text := m.getLocalClipboard()
			if text == "" {
				continue
			}
			hash := fmt.Sprintf("%d:%s", len(text), text[:min(100, len(text))])
			m.mu.Lock()
			changed := hash != m.lastHash
			if changed {
				m.lastHash = hash
			}
			m.mu.Unlock()

			if changed {
				slog.Info("clipboard changed, sending to remote", "len", len(text))
				m.wg.Add(1)
				go func(t string) {
					defer m.wg.Done()
					m.sendText(t)
				}(text)
			}
		}
	}
}

// sendClipboard sends the current clipboard to the remote.
func (m *Manager) sendClipboard() {
	text := m.getLocalClipboard()
	if text != "" {
		m.sendText(text)
	}
}

// sendText sends text to the remote via ClipboardText packets.
func (m *Manager) sendText(text string) {
	// Prepend format marker: "TXT" + text
	// MWB uses multi-format with GUID separator, but for simplicity we just send TXT
	markedText := "TXT" + text
	utf16 := encodeUTF16LE(markedText)

	// Deflate compress
	compressed, err := deflateCompress(utf16)
	if err != nil {
		slog.Error("clipboard compress failed", "err", err)
		return
	}

	if len(compressed) > maxInlineSize {
		slog.Warn("clipboard too large for inline send", "size", len(compressed))
		return
	}

	if !m.broadcast(clipboardPackets(protocol.ClipboardText, compressed)) {
		return
	}

	slog.Info("clipboard sent to remote", "chunks", (len(compressed)+dataSize-1)/dataSize)
}

// handleChunk accumulates a clipboard data chunk.
func (m *Manager) handleChunk(pkt *protocol.Packet) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.receiving {
		m.recvBuf.Reset()
		m.receiving = true
		m.recvIsImage = (pkt.Type == protocol.ClipboardImage)
	}
	if pkt.ClipboardData != nil {
		if m.recvBuf.Len()+len(pkt.ClipboardData) > maxRecvBuf {
			slog.Warn("clipboard receive buffer exceeded limit, dropping stream", "limit", maxRecvBuf)
			m.recvBuf.Reset()
			m.receiving = false
			return
		}
		m.recvBuf.Write(pkt.ClipboardData)
	}
}

// handleEnd processes the complete clipboard data.
func (m *Manager) handleEnd(pkt *protocol.Packet) {
	m.mu.Lock()
	data := make([]byte, m.recvBuf.Len())
	copy(data, m.recvBuf.Bytes())
	isImage := m.recvIsImage
	m.recvBuf.Reset()
	m.receiving = false
	m.mu.Unlock()

	if len(data) == 0 {
		return
	}
	m.handleRemoteData(data, isImage)
}

// HandleFileChannelPayload applies oversized text/image content received over
// the file channel. PowerToys uses the same compressed-text and raw-image
// formats as the control channel; only the transport differs.
func (m *Manager) HandleFileChannelPayload(name string, data []byte) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "image"):
		m.handleRemoteData(data, true)
	case strings.HasPrefix(lower, "text"):
		m.handleRemoteData(data, false)
	default:
		slog.Warn("unknown inline clipboard payload", "name", name, "bytes", len(data))
	}
}

func (m *Manager) handleRemoteData(data []byte, isImage bool) {

	if isImage {
		// Try decompress first, fall back to raw data
		decompressed, err := deflateDecompress(data)
		if err != nil {
			if errors.Is(err, errDecompressedTooLarge) {
				slog.Warn("rejected oversized image clipboard", "err", err, "dataLen", len(data))
				return
			}
			slog.Info("image clipboard not deflate-compressed, using raw data", "dataLen", len(data))
			m.handleImageClipboard(data)
		} else {
			m.handleImageClipboard(decompressed)
		}
		return
	}

	// Text clipboard — always Deflate compressed
	decompressed, err := deflateDecompress(data)
	if err != nil {
		slog.Error("clipboard decompress failed", "err", err, "dataLen", len(data))
		return
	}

	// Decode UTF-16LE to string
	text := decodeUTF16LE(decompressed)

	// Parse multi-format: split on TEXT_TYPE_SEP, find TXT section
	parts := strings.Split(text, textTypeSep)
	plainText := ""
	for _, part := range parts {
		if strings.HasPrefix(part, "TXT") {
			plainText = strings.TrimPrefix(part, "TXT")
			break
		}
	}
	if plainText == "" && len(parts) > 0 {
		plainText = text
	}

	if plainText == "" {
		return
	}

	// Update our hash so we don't re-send what we just received
	hash := fmt.Sprintf("%d:%s", len(plainText), plainText[:min(100, len(plainText))])
	m.mu.Lock()
	m.lastHash = hash
	m.mu.Unlock()

	// Set local clipboard
	m.setLocalClipboard(plainText)
	m.mu.Lock()
	m.justSet = time.Now()
	m.mu.Unlock()
	slog.Info("clipboard text received from remote", "len", len(plainText))
}

// handleImageClipboard processes received image data and sets it to clipboard.
func (m *Manager) handleImageClipboard(data []byte) {
	slog.Info("processing image clipboard", "rawSize", len(data))

	// MWB may send raw BMP data — detect by header
	// BMP starts with "BM", PNG starts with 0x89504E47
	imgData := data
	mimeType := "image/bmp"

	if len(data) > 4 {
		if data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47 {
			mimeType = "image/png"
		} else if data[0] == 'B' && data[1] == 'M' {
			mimeType = "image/bmp"
		} else {
			// Might be raw DIB (no BM header) — add BMP header
			slog.Info("image data doesn't have known header, trying as raw DIB",
				"first4", fmt.Sprintf("%02x %02x %02x %02x", data[0], data[1], data[2], data[3]))
			mimeType = "image/bmp"
		}
	}

	// Feed image bytes directly to xclip. Avoiding a temporary file eliminates
	// predictable-path, symlink, and local clipboard-disclosure risks entirely.
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", mimeType, "-i")
	cmd.Env = append(os.Environ(), "DISPLAY="+m.display)
	cmd.Stdin = bytes.NewReader(imgData)
	err := cmd.Run()
	cancel()
	if err != nil {
		slog.Error("set image clipboard via xclip failed", "err", err, "mime", mimeType)
		ctx2, cancel2 := context.WithTimeout(context.Background(), execTimeout)
		cmd2 := exec.CommandContext(ctx2, "xclip", "-selection", "clipboard", "-t", "image/png", "-i")
		cmd2.Env = append(os.Environ(), "DISPLAY="+m.display)
		cmd2.Stdin = bytes.NewReader(imgData)
		if err2 := cmd2.Run(); err2 != nil {
			slog.Error("set image clipboard fallback also failed", "err", err2)
		}
		cancel2()
		return
	}

	m.mu.Lock()
	m.justSet = time.Now()
	// Also update lastHash so pollClipboard doesn't re-send after the 3s suppress
	// window expires — without this, the same image echoes back to Windows.
	m.lastHash = fmt.Sprintf("img:%d", len(data))
	m.mu.Unlock()
	slog.Info("clipboard image received from remote", "size", len(data), "mime", mimeType)
}

// getLocalClipboard reads the current clipboard text.
// Times out after execTimeout to prevent blocking the poll goroutine indefinitely.
func (m *Manager) getLocalClipboard() string {
	if m.wayland {
		out, err := wlPaste("text")
		if err != nil {
			return ""
		}
		return string(out)
	}
	for _, args := range [][]string{
		{"xclip", "-selection", "clipboard", "-o"},
		{"xsel", "--clipboard", "--output"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "DISPLAY="+m.display)
		out, err := cmd.Output()
		cancel()
		if err == nil {
			return string(out)
		}
	}
	return ""
}

// setLocalClipboard sets the clipboard text.
// Times out after execTimeout to prevent blocking on a hung xclip/xsel.
func (m *Manager) setLocalClipboard(text string) {
	for _, args := range [][]string{
		{"xclip", "-selection", "clipboard"},
		{"xsel", "--clipboard", "--input"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "DISPLAY="+m.display)
		cmd.Stdin = strings.NewReader(text)
		err := cmd.Run()
		cancel()
		if err == nil {
			return
		}
	}
	slog.Error("set clipboard failed — both xclip and xsel failed")
}

// getLocalImageClipboard checks if clipboard contains an image and returns it.
func (m *Manager) getLocalImageClipboard() []byte {
	out, err := m.readClipboard("TARGETS")
	if err != nil || !strings.Contains(string(out), "image/png") {
		return nil
	}
	imgData, err := m.readClipboard("image/png")
	if err != nil || len(imgData) == 0 {
		return nil
	}
	return imgData
}

// sendImage sends image data to the remote via ClipboardImage packets.
func (m *Manager) sendImage(data []byte) {
	if len(data) > maxInlineSize {
		slog.Warn("image too large for inline send", "size", len(data))
		return
	}

	if !m.broadcast(clipboardPackets(protocol.ClipboardImage, data)) {
		return
	}

	slog.Info("image clipboard sent to remote", "chunks", (len(data)+dataSize-1)/dataSize)
}

// clipboardPackets splits data into 48-byte chunks of type t, followed by
// the end marker.
func clipboardPackets(t protocol.PackageType, data []byte) []*protocol.Packet {
	var pkts []*protocol.Packet
	for offset := 0; offset < len(data); offset += dataSize {
		chunk := data[offset:min(offset+dataSize, len(data))]
		pkt := &protocol.Packet{Type: t, Des: protocol.IDAll}
		pkt.ClipboardData = make([]byte, dataSize)
		copy(pkt.ClipboardData, chunk)
		pkts = append(pkts, pkt)
	}
	end := &protocol.Packet{Type: protocol.ClipboardDataEnd, Des: protocol.IDAll}
	end.ClipboardData = make([]byte, dataSize)
	return append(pkts, end)
}

// readClipboard returns the local clipboard's content of type target;
// "TARGETS" lists the offered types, one per line.
func (m *Manager) readClipboard(target string) ([]byte, error) {
	if m.wayland {
		if target == "TARGETS" {
			return wlPaste("")
		}
		return wlPaste(target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", target, "-o")
	cmd.Env = append(os.Environ(), "DISPLAY="+m.display)
	return cmd.Output()
}

// encodeUTF16LE encodes a Go string to UTF-16LE bytes.
func encodeUTF16LE(s string) []byte {
	var buf bytes.Buffer
	for _, r := range s {
		if r > 0xFFFF {
			// Surrogate pair for supplementary characters
			r -= 0x10000
			hi := uint16(0xD800 + (r>>10)&0x3FF)
			lo := uint16(0xDC00 + r&0x3FF)
			_ = binary.Write(&buf, binary.LittleEndian, hi)
			_ = binary.Write(&buf, binary.LittleEndian, lo)
		} else {
			_ = binary.Write(&buf, binary.LittleEndian, uint16(r))
		}
	}
	return buf.Bytes()
}

// decodeUTF16LE decodes UTF-16LE bytes to a Go string.
func decodeUTF16LE(b []byte) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	var runes []rune
	for i := 0; i < len(b); i += 2 {
		u := uint16(b[i]) | uint16(b[i+1])<<8
		if u == 0 {
			break // null terminator
		}
		if u >= 0xD800 && u <= 0xDBFF && i+2 < len(b) {
			// High surrogate
			lo := uint16(b[i+2]) | uint16(b[i+3])<<8
			if lo >= 0xDC00 && lo <= 0xDFFF {
				r := rune((uint32(u)-0xD800)*0x400 + (uint32(lo) - 0xDC00) + 0x10000)
				runes = append(runes, r)
				i += 2
				continue
			}
		}
		runes = append(runes, rune(u))
	}
	return string(runes)
}

// deflateCompress compresses data using Deflate.
func deflateCompress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// deflateDecompress decompresses Deflate data, refusing output larger than
// maxDecompressedSize to guard against decompression bombs.
func deflateDecompress(data []byte) ([]byte, error) {
	return deflateDecompressLimit(data, maxDecompressedSize)
}

func deflateDecompressLimit(data []byte, limit int64) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close() //nolint:errcheck
	// Read one byte past the limit so we can detect an over-limit stream.
	out, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: %d bytes", errDecompressedTooLarge, limit)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
