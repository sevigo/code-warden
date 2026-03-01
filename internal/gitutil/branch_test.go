package gitutil

import (
	"testing"
)

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{"valid simple", "feature/login", false},
		{"valid with dots", "release/v1.2.3", false},
		{"valid with hyphens", "fix/issue-42", false},
		{"valid single char", "a", false},
		{"valid alphanumeric", "abc123", false},
		{"empty", "", true},
		{"starts with hyphen", "-bad", true},
		{"starts with dot", ".bad", true},
		{"ends with hyphen", "bad-", true},
		{"ends with dot", "bad.", true},
		{"contains spaces", "bad name", true},
		{"contains consecutive dots", "bad..name", true},
		{"contains tilde", "bad~name", true},
		{"contains caret", "bad^name", true},
		{"contains colon", "bad:name", true},
		{"contains backslash", "bad\\name", true},
		{"too long", string(make([]byte, 256)), true},
		{"max length", string(make([]byte, 255)), true}, // all zero bytes are invalid chars
		{"valid agent branch", "agent/issue-123-1234567890", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBranchName(tt.branch)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateBranchName(%q) error = %v, wantErr %v", tt.branch, err, tt.wantErr)
			}
		})
	}
}

func TestSanitizeBranch(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean name", "feature/login", "feature/login"},
		{"spaces to hyphens", "feature login", "feature-login"},
		{"tilde removed", "feature~test", "feature-test"},
		{"leading hyphen stripped", "-feature", "feature"},
		{"trailing hyphen stripped", "feature-", "feature"},
		{"leading dot stripped", ".feature", "feature"},
		{"trailing dot stripped", "feature.", "feature"},
		{"multiple invalid chars", "feat~ure^test:name", "feat-ure-test-name"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeBranch(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeBranch(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
