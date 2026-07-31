package filetransfer

import (
	"errors"
	"fmt"
	"io"
)

// blockSize is the AES-CBC block the transport encrypts in.
//
// The stream is raw CBC with no padding scheme: EncryptWriter refuses a write
// that is not a whole number of blocks, and DecryptReader always consumes a
// whole block, discarding anything past the caller's buffer. So a file whose
// length is not a multiple of the block size must still be moved as whole
// blocks, with the sender zero-filling the tail and the receiver keeping only
// the declared number of bytes. Getting this wrong loses the tail of every file
// and leaves the connection desynchronised for anything after it.
const blockSize = 16

// copyBufSize is a multiple of blockSize so every read stays aligned.
const copyBufSize = 64 * 1024

// alignedLen rounds n up to a whole number of blocks.
func alignedLen(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return (n + blockSize - 1) / blockSize * blockSize
}

// writeAligned copies size bytes from r to w, zero-filling the final block so
// the transport only ever sees whole blocks.
func writeAligned(w io.Writer, r io.Reader, size int64) (int64, error) {
	buf := make([]byte, copyBufSize)
	var sent int64

	for sent < size {
		want := size - sent
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		if _, err := io.ReadFull(r, buf[:want]); err != nil {
			return sent, fmt.Errorf("read source: %w", err)
		}

		out := alignedLen(want)
		// Clear the padding rather than leaking whatever the buffer held.
		for i := want; i < out; i++ {
			buf[i] = 0
		}
		if _, err := w.Write(buf[:out]); err != nil {
			return sent, err
		}
		sent += want
	}
	return sent, nil
}

// readAligned reads alignedLen(size) bytes from r and writes the first size of
// them to w, discarding the sender's zero padding.
func readAligned(w io.Writer, r io.Reader, size int64) (int64, error) {
	buf := make([]byte, copyBufSize)
	var consumed, written int64
	total := alignedLen(size)

	for consumed < total {
		want := total - consumed
		if want > int64(len(buf)) {
			want = int64(len(buf))
		}
		if _, err := io.ReadFull(r, buf[:want]); err != nil {
			// A peer that closes early is short, not broken. Name it so callers
			// can distinguish truncation from a genuine transport failure.
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return written, fmt.Errorf("%w: got %d of %d bytes", ErrShortBody, written, size)
			}
			return written, err
		}

		// Keep only the bytes that are inside the declared size; the rest is
		// padding the sender added to reach a block boundary.
		keep := want
		if consumed+keep > size {
			keep = size - consumed
		}
		if keep > 0 {
			if _, err := w.Write(buf[:keep]); err != nil {
				return written, err
			}
			written += keep
		}
		consumed += want
	}
	return written, nil
}
