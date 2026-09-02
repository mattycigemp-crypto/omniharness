// Package id generates unique identifiers for sessions, tasks, agents and
// events. IDs are random hex strings, 16 bytes of entropy.
package id

import (
	"crypto/rand"
	"encoding/hex"
)

// New returns a new random identifier: 32 lowercase hex characters.
//
// crypto/rand.Read is documented never to return an error — it crashes the
// program rather than hand back short or predictable bytes — so there is no
// degraded mode here. An earlier version fell back to a formatted timestamp,
// which was both unreachable and a promise of something weaker than what is
// actually produced: guessable, and a different shape from every other id.
func New() string {
	var b [16]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
