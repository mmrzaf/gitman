// Package id generates identifiers used as primary keys throughout Gitman.
// These identifiers are unique, not secret: session tokens, access tokens
// and other values that must resist guessing are generated separately,
// where a failure to obtain randomness can be treated as fatal instead of
// falling back to a weaker source.
package id

import (
	"crypto/rand"
	"fmt"
	"sync/atomic"
	"time"
)

var fallbackCounter atomic.Uint64

// New returns a unique identifier suitable as a primary key: a random UUID
// (version 4, variant RFC 4122) in the overwhelming majority of calls. If
// the system's random source is unavailable, it falls back to a timestamp
// and a process-local counter so identifier generation itself is never the
// reason a request fails. The fallback value is not cryptographically
// random and must never be used as a secret.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		b[6] = (b[6] & 0x0f) | 0x40
		b[8] = (b[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	}
	return fmt.Sprintf("fallback-%x-%x", time.Now().UnixNano(), fallbackCounter.Add(1))
}
