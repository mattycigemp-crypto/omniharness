// Package id generates unique identifiers for sessions, tasks, agents and
// events. IDs are random hex strings, 16 bytes of entropy.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// New returns a new random identifier.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	return hex.EncodeToString(b[:])
}
