// Package idgen generates opaque identifiers without external dependencies.
package idgen

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewUUID returns a random RFC 4122 version 4 UUID string.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is effectively impossible on supported platforms;
		// fall back to a time-based value so callers always get an identifier.
		return fmt.Sprintf("%016x-0000-4000-8000-000000000000", uint64(time.Now().UnixNano()))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
