// internal/protocol/stream.go
package protocol

import (
	"crypto/aes"
	"crypto/cipher"
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
