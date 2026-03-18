package index

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sevigo/code-warden/internal/llm"
)

// ComputeFileHash returns the SHA-256 hex digest of a file.
func ComputeFileHash(path string) (string, error) {
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

// IsTestFile returns true if the specified file path seems to be a test file based on conventions.
func IsTestFile(path string) bool {
	ext := filepath.Ext(path)
	base := filepath.Base(path)

	switch ext {
	case extGo:
		return strings.HasSuffix(base, "_test.go")
	case extTypeScript, extTSX:
		return strings.HasSuffix(base, ".test.ts") || strings.HasSuffix(base, ".spec.ts") ||
			strings.HasSuffix(base, ".test.tsx") || strings.HasSuffix(base, ".spec.tsx")
	case extJavaScript, extJSX:
		return strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".spec.js") ||
			strings.HasSuffix(base, ".test.jsx") || strings.HasSuffix(base, ".spec.jsx")
	case extPython:
		return strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")
	case extRust:
		return strings.HasSuffix(base, "_test.rs") // Rust conventions vary but often in-file or test_*.rs
	case extJava:
		return strings.HasSuffix(base, "Test.java") || strings.HasSuffix(base, "Tests.java")
	}
	return false
}

// IsLogicFile returns true if the file has a recognized code extension.
func IsLogicFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return llm.IsCodeExtension(ext)
}
