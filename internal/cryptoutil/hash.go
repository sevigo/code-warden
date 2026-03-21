package cryptoutil

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// HashString returns the SHA-256 hex digest of a string (64 characters).
func HashString(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// HashStringShort returns the first 16 bytes of SHA-256 hex digest (32 characters).
// Useful for cache keys where full hash length isn't necessary.
func HashStringShort(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

// HashFile returns the SHA-256 hex digest of a file's contents.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
