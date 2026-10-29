// internal/config/keygen.go
package config

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// GeneratedKeyLength is the length of keys from GenerateKey. PowerToys accepts
// 16 or more characters; 24 alphanumerics give about 142 bits of entropy.
const GeneratedKeyLength = 24

const keyAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// GenerateKey returns a random security key suitable for both mwb-linux and
// PowerToys peers. It uses letters and digits only so the key can be typed
// into the PowerToys settings page and stored in TOML without escaping.
func GenerateKey() (string, error) {
	out := make([]byte, GeneratedKeyLength)
	limit := big.NewInt(int64(len(keyAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit) // uniform; no modulo bias
		if err != nil {
			return "", fmt.Errorf("generate key: %w", err)
		}
		out[i] = keyAlphabet[n.Int64()]
	}
	return string(out), nil
}
