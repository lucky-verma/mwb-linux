// internal/config/keygen_test.go
package config

import (
	"strings"
	"testing"
)

func TestGenerateKey(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		k, err := GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		if len(k) != GeneratedKeyLength {
			t.Fatalf("key length %d, want %d", len(k), GeneratedKeyLength)
		}
		for _, r := range k {
			if !strings.ContainsRune(keyAlphabet, r) {
				t.Fatalf("key %q contains %q outside the alphanumeric alphabet", k, r)
			}
		}
		if seen[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seen[k] = true
	}
}
