package platform

import (
	"crypto/rand"
	"encoding/hex"
)

// randomSuffix returns a 64-bit random hex suffix for private temp names.
func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable for private-name generation;
		// returning a constant would break collision retry loops loudly
		// rather than silently reusing names.
		panic("platform: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
