// Package token makes and hashes the secret tokens Gitman issues —
// session cookies, access tokens, one-time passwords and run fetch
// tokens — so there is one implementation of each security-sensitive
// operation. A token's plain value is handed out once; only its hash is
// ever stored, so a database leak alone exposes no usable credential.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// New returns a random URL-safe token built from n random bytes.
func New(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns the SHA-256 hash of a token, hex-encoded, for storage.
func Hash(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}
