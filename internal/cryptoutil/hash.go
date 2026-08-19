package cryptoutil

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashString returns the SHA-256 hex digest of a string (64 characters).
func HashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
