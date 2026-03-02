package review

import (
	"testing"
)

func TestHashDiff(t *testing.T) {
	// Deterministic: same input produces same hash
	hash1 := hashDiff("diff --git a/file.go")
	hash2 := hashDiff("diff --git a/file.go")
	if hash1 != hash2 {
		t.Errorf("same input produced different hashes: %s vs %s", hash1, hash2)
	}

	// Different input produces different hash
	hash3 := hashDiff("diff --git a/other.go")
	if hash1 == hash3 {
		t.Errorf("different inputs produced same hash: %s", hash1)
	}

	// Hash is a valid hex string of expected length (SHA-256 = 64 hex chars)
	if len(hash1) != 64 {
		t.Errorf("expected 64 char hex hash, got %d chars: %s", len(hash1), hash1)
	}

	// Empty diff still produces a valid hash
	hashEmpty := hashDiff("")
	if len(hashEmpty) != 64 {
		t.Errorf("empty diff should produce valid hash, got %d chars", len(hashEmpty))
	}
}
