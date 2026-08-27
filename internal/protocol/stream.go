// internal/protocol/stream.go
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

// EncryptWriter wraps an io.Writer with AES-256-CBC encryption.
type EncryptWriter struct {
	w    io.Writer
	mode cipher.BlockMode
}

// NewEncryptWriter creates an AES-256-CBC encrypting writer.
func NewEncryptWriter(w io.Writer, key, iv []byte) (*EncryptWriter, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCEncrypter(block, iv)
	return &EncryptWriter{w: w, mode: mode}, nil
}

// Write encrypts data and writes to the underlying writer.
// Data length must be a multiple of 16 (AES block size).
func (e *EncryptWriter) Write(p []byte) (int, error) {
	if len(p)%aesBlockSize != 0 {
		return 0, fmt.Errorf("data length %d not multiple of %d", len(p), aesBlockSize)
	}
	ct := make([]byte, len(p))
	e.mode.CryptBlocks(ct, p)
	return e.w.Write(ct)
}

// DecryptReader wraps an io.Reader with AES-256-CBC decryption.
type DecryptReader struct {
	r    io.Reader
	mode cipher.BlockMode
}

// NewDecryptReader creates an AES-256-CBC decrypting reader.
func NewDecryptReader(r io.Reader, key, iv []byte) (*DecryptReader, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher: %w", err)
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	return &DecryptReader{r: r, mode: mode}, nil
}

// Read reads encrypted data, decrypts it, and returns plaintext.
// Reads in multiples of 16 bytes.
func (d *DecryptReader) Read(p []byte) (int, error) {
	n := len(p)
	if n < aesBlockSize {
		n = aesBlockSize
	}
	n = (n / aesBlockSize) * aesBlockSize

	ct := make([]byte, n)
	nn, err := io.ReadFull(d.r, ct)
	if err != nil {
		return 0, err
	}
	d.mode.CryptBlocks(ct[:nn], ct[:nn])
	copy(p, ct[:nn])
	if nn > len(p) {
		return len(p), nil
	}
	return nn, nil
}

// NewEncryptWriterWithHeader starts an outbound PowerToys-compatible encrypted
// stream: it generates a random per-connection salt and IV, writes them to w in
// the clear, then returns a writer keyed from securityKey and that salt.
//
// The header goes out before any ciphertext, so callers must create the writer
// (and flush anything they owe the peer) before blocking on a read — see
// NewDecryptReaderWithHeader.
func NewEncryptWriterWithHeader(w io.Writer, securityKey string) (*EncryptWriter, error) {
	header := make([]byte, HeaderSize)
	if _, err := rand.Read(header); err != nil {
		return nil, fmt.Errorf("generate encryption header: %w", err)
	}
	if _, err := w.Write(header); err != nil {
		return nil, fmt.Errorf("send encryption header: %w", err)
	}
	return NewEncryptWriter(w, DeriveKeyWithSalt(securityKey, header[:SaltSize]), header[SaltSize:])
}

// NewDecryptReaderWithHeader reads the peer's cleartext salt+IV header from r
// and returns a reader that decrypts the rest of the stream with the key that
// header implies. It blocks until the peer has sent its header.
func NewDecryptReaderWithHeader(r io.Reader, securityKey string) (*DecryptReader, error) {
	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, fmt.Errorf("read encryption header: %w", err)
	}
	return NewDecryptReader(r, DeriveKeyWithSalt(securityKey, header[:SaltSize]), header[SaltSize:])
}
