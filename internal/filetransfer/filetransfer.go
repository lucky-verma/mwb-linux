// Package filetransfer implements MWB's file copy channel.
//
// File bytes do not travel over the 32/64-byte control packet stream. MWB runs
// two listeners and files use the other one:
//
//	skMessageServer   = new TcpServer(TcpPort + 1, TCPServerThread);
//	skClipboardServer = new TcpServer(TcpPort,     AcceptConnectionAndSendClipboardData);
//
// A file channel is a second TCP connection to the *base* port, not to the
// control port at base+1. It performs the same IV exchange and AES-CBC stream
// setup as the control channel, sends one raw unstamped 64-byte DATA struct
// with type Clipboard or ClipboardPush, and then sends:
//
//	a 1024-byte UTF-16LE header holding "<size>*<name>", null padded
//	exactly <size> raw bytes of file content
//
// Everything in this package operates on the already-decrypted stream. Setting
// up crypto and reading the DATA packet is the caller's job.
package filetransfer

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// HeaderSize is the fixed width of the name/size header, matching the
// `new byte[1024]` MWB reads before the file body.
const HeaderSize = 1024

// DefaultMaxSize matches MWB's own MAX_CLIPBOARD_FILE_SIZE_CAN_BE_SENT. A stock
// Windows peer neither sends nor accepts more, so a larger value only has
// meaning when talking to a non-MWB implementation.
const DefaultMaxSize int64 = 100 * 1024 * 1024

// separator divides size from name inside the header. MWB splits on '*', which
// is illegal in Windows filenames but legal on Linux, so only the first
// occurrence is treated as the separator.
const separator = "*"

var (
	// ErrHeaderTooShort means the peer sent fewer than HeaderSize bytes.
	ErrHeaderTooShort = errors.New("file header truncated")
	// ErrHeaderMalformed means the header was not "<size>*<name>".
	ErrHeaderMalformed = errors.New("file header malformed")
	// ErrSizeRejected means the declared size was negative or over the cap.
	ErrSizeRejected = errors.New("declared file size rejected")
	// ErrShortBody means the peer closed before sending the declared bytes.
	ErrShortBody = errors.New("file body shorter than declared size")
	// ErrPeerRejected means PowerToys sent a zero-byte status header instead of
	// a file. These headers are protocol errors, not valid clipboard files.
	ErrPeerRejected = errors.New("peer refused file transfer")
)

// Header describes one incoming or outgoing file.
type Header struct {
	Size int64
	Name string // as sent by the peer; NOT yet safe to use as a path
}

// EncodeHeader renders a header into its fixed-width wire form.
func EncodeHeader(h Header) ([]byte, error) {
	if h.Size < 0 {
		return nil, fmt.Errorf("%w: negative size %d", ErrSizeRejected, h.Size)
	}
	if strings.ContainsAny(h.Name, "\x00") {
		return nil, fmt.Errorf("%w: name contains NUL", ErrHeaderMalformed)
	}

	text := strconv.FormatInt(h.Size, 10) + separator + h.Name
	encoded := utf16.Encode([]rune(text))

	// Two bytes per code unit, and the trailing NUL padding must still fit.
	if len(encoded)*2 > HeaderSize-2 {
		return nil, fmt.Errorf("%w: name too long for a %d byte header", ErrHeaderMalformed, HeaderSize)
	}

	buf := make([]byte, HeaderSize)
	for i, u := range encoded {
		buf[i*2] = byte(u)
		buf[i*2+1] = byte(u >> 8)
	}
	return buf, nil
}

// ParseHeader decodes the fixed-width header a peer sent.
//
// maxSize bounds the declared size. The declared size is advisory: ReceiveFile
// also stops reading at the cap, so a peer that lies about its size cannot
// write more than the cap allows.
func ParseHeader(buf []byte, maxSize int64) (Header, error) {
	if len(buf) < HeaderSize {
		return Header{}, fmt.Errorf("%w: got %d of %d bytes", ErrHeaderTooShort, len(buf), HeaderSize)
	}

	units := make([]uint16, 0, HeaderSize/2)
	for i := 0; i+1 < HeaderSize; i += 2 {
		u := uint16(buf[i]) | uint16(buf[i+1])<<8
		if u == 0 {
			break // NUL padding starts here
		}
		units = append(units, u)
	}
	text := string(utf16.Decode(units))

	// A malformed or non-UTF-16 header can decode to invalid runes. Reject
	// rather than carry replacement characters into a filename.
	if !utf8.ValidString(text) || strings.ContainsRune(text, utf8.RuneError) {
		return Header{}, fmt.Errorf("%w: undecodable header text", ErrHeaderMalformed)
	}

	sizeText, name, found := strings.Cut(text, separator)
	if !found {
		return Header{}, fmt.Errorf("%w: no %q separator in %q", ErrHeaderMalformed, separator, text)
	}

	size, err := strconv.ParseInt(strings.TrimSpace(sizeText), 10, 64)
	if err != nil {
		return Header{}, fmt.Errorf("%w: size %q: %v", ErrHeaderMalformed, sizeText, err)
	}
	if size < 0 {
		return Header{}, fmt.Errorf("%w: negative size %d", ErrSizeRejected, size)
	}
	if maxSize > 0 && size > maxSize {
		return Header{}, fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrSizeRejected, size, maxSize)
	}
	if name == "" {
		return Header{}, fmt.Errorf("%w: empty name", ErrHeaderMalformed)
	}

	return Header{Size: size, Name: name}, nil
}

// IsInlinePayload reports whether a header names clipboard content rather than a
// file. MWB reuses this same channel for large text and image clipboard
// payloads, marking them with a name beginning "text" or "image", and expects
// the receiver to keep those in memory instead of writing them to disk.
func IsInlinePayload(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "text") || strings.HasPrefix(lower, "image")
}

// peerTransferError recognises the status headers emitted by PowerToys when a
// clipboard file cannot be transferred. PowerToys puts the explanation in the
// filename field and declares a zero-byte body; accepting that as a file turns
// the error sentence into a real clipboard item.
//
// Size is part of the signature deliberately. Empty files are valid and must
// continue to round-trip unless their names match an exact PowerToys status.
func peerTransferError(h Header) error {
	if h.Size != 0 {
		return nil
	}
	for _, suffix := range [...]string{
		" - File too big (greater than 100MB), please drag and drop the file instead!",
		" - Folder is not supported, zip it first!",
		" not found!",
	} {
		if strings.HasSuffix(h.Name, suffix) {
			return fmt.Errorf("%w: %s", ErrPeerRejected, h.Name)
		}
	}
	return nil
}

// readFull reads exactly len(buf) bytes, mapping a short read to a named error.
func readFull(r io.Reader, buf []byte) error {
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return ErrHeaderTooShort
		}
		return err
	}
	return nil
}
