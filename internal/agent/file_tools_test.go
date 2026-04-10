package agent

import (
	"testing"
)

// TestSafeJoin verifies that safeJoin allows valid paths and rejects traversal.
func TestSafeJoin(t *testing.T) {
	t.Parallel()
	root := "/workspace"

	tests := []struct {
		name    string
		relPath string
		wantErr bool
	}{
		{"simple file", "main.go", false},
		{"nested file", "internal/agent/warden.go", false},
		{"root itself", ".", false},
		{"absolute escape", "../etc/passwd", true},
		{"double escape", "../../root/.ssh/id_rsa", true},
		{"escaped after subdir", "sub/../../etc/passwd", true},
		{"valid subdir", "sub/dir/file.go", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := safeJoin(root, tc.relPath)
			if (err != nil) != tc.wantErr {
				t.Errorf("safeJoin(%q, %q) error = %v, wantErr = %v", root, tc.relPath, err, tc.wantErr)
			}
		})
	}
}
